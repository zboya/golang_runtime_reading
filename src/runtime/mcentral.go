// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Central free lists.
//
// See malloc.go for an overview.
//
// The MCentral doesn't actually contain the list of free objects; the MSpan does.
// Each MCentral is two lists of MSpans: those with free objects (c->nonempty)
// and those that are completely allocated (c->empty).

package runtime

import "runtime/internal/atomic"

// Central list of free objects of a given size.
//
//go:notinheap
type mcentral struct {
	lock      mutex
	spanclass spanClass
	// 这个a free object不是一个的意思。应该是若干个。
	nonempty mSpanList // list of spans with a free object, ie a nonempty free list
	empty    mSpanList // list of spans with no free objects (or cached in an mcache)

	// nmalloc is the cumulative count of objects allocated from
	// this mcentral, assuming all spans in mcaches are
	// fully-allocated. Written atomically, read under STW.
	nmalloc uint64
}

// Initialize a single central free list.
func (c *mcentral) init(spc spanClass) {
	c.spanclass = spc
	c.nonempty.init()
	c.empty.init()
}

// Allocate a span to use in an MCache.
// 分配span，规格在mcentral中
func (c *mcentral) cacheSpan() *mspan {
	// 扣除这个span的借贷，如果有必要进行清扫。
	// 大致是分配的时候做一些gc工作
	// Deduct credit for this span allocation and sweep if necessary.
	spanBytes := uintptr(class_to_allocnpages[c.spanclass.sizeclass()]) * _PageSize
	deductSweepCredit(spanBytes, 0)

	lock(&c.lock)
	traceDone := false
	if trace.enabled {
		traceGCSweepStart()
	}
	// sweep generation:
	// if sweepgen == h->sweepgen - 2, the span needs sweeping
	// if sweepgen == h->sweepgen - 1, the span is currently being swept
	// if sweepgen == h->sweepgen, the span is swept and ready to use
	// h->sweepgen is incremented by 2 after every GC
	sg := mheap_.sweepgen
retry:
	var s *mspan
	// 遍历有空闲对象的链表
	// 如果需要清扫，进行清扫并把它移到空span链表(分配给下一级的mcache)
	for s = c.nonempty.first; s != nil; s = s.next {
		if s.sweepgen == sg-2 && atomic.Cas(&s.sweepgen, sg-2, sg-1) {
			c.nonempty.remove(s)
			c.empty.insertBack(s)
			unlock(&c.lock)
			s.sweep(true)
			goto havespan
		}
		if s.sweepgen == sg-1 {
			// 正在清扫，下一个
			// the span is being swept by background sweeper, skip
			continue
		}
		// 请扫过了 可以直接用
		// we have a nonempty span that does not require sweeping, allocate from it
		c.nonempty.remove(s)
		c.empty.insertBack(s)
		unlock(&c.lock)
		goto havespan
	}
	// 从empty中找一找，这里面的要么没空闲对象，要么在mcache中
	for s = c.empty.first; s != nil; s = s.next {
		// 需要清扫
		if s.sweepgen == sg-2 && atomic.Cas(&s.sweepgen, sg-2, sg-1) {
			// we have an empty span that requires sweeping,
			// sweep it and see if we can free some space in it
			c.empty.remove(s)
			// swept spans are at the end of the list
			c.empty.insertBack(s)
			unlock(&c.lock)
			s.sweep(true)
			freeIndex := s.nextFreeIndex()
			if freeIndex != s.nelems {
				s.freeindex = freeIndex
				goto havespan
			}
			lock(&c.lock)
			// the span is still empty after sweep
			// it is already in the empty list, so just retry
			goto retry
		}
		if s.sweepgen == sg-1 {
			// the span is being swept by background sweeper, skip
			continue
		}
		// already swept empty span,
		// all subsequent ones must also be either swept or in process of sweeping
		break
	}
	if trace.enabled {
		traceGCSweepDone()
		traceDone = true
	}
	unlock(&c.lock)

	// 还是没有，只能grow了，从heap中分配
	// Replenish central list if empty.
	s = c.grow()
	if s == nil {
		return nil
	}
	lock(&c.lock)
	c.empty.insertBack(s)
	unlock(&c.lock)

	// At this point s is a non-empty span, queued at the end of the empty list,
	// c is unlocked.
havespan:
	if trace.enabled && !traceDone {
		traceGCSweepDone()
	}
	// 页数*页大小/元素大小
	cap := int32((s.npages << _PageShift) / s.elemsize)
	n := cap - int32(s.allocCount)
	if n == 0 || s.freeindex == s.nelems || uintptr(s.allocCount) == s.nelems {
		throw("span has no free objects")
	}
	// 假设这个span的所有的对象都会被分配到mcache中。如果未缓存，我们会调整的
	// heap_live是gc触发的参数，这里似乎是默认这个span全都被分配了，
	// 这样会使heap_live偏高(毕竟这个span还没有真的用完)，算是宁缺毋滥吧。
	// Assume all objects from this span will be allocated in the
	// mcache. If it gets uncached, we'll adjust this.
	atomic.Xadd64(&c.nmalloc, int64(n))
	usedBytes := uintptr(s.allocCount) * s.elemsize
	atomic.Xadd64(&memstats.heap_live, int64(spanBytes)-int64(usedBytes))
	if trace.enabled {
		// heap_live changed.
		traceHeapAlloc()
	}
	if gcBlackenEnabled != 0 {
		// heap_live changed.
		gcController.revise()
	}
	// 已经被一个span使用了
	s.incache = true
	// 向上取64的倍数
	// 接下来调整下。
	// 查不到的时候继续从头查找就行
	freeByteBase := s.freeindex &^ (64 - 1)
	whichByte := freeByteBase / 8
	// Init alloc bits cache.
	// 初始化缓存
	s.refillAllocCache(whichByte)

	// 调节一下，右移动,allocCache的1表示未使用，0表示已使用
	// Adjust the allocCache so that s.freeindex corresponds to the low bit in
	// s.allocCache.
	s.allocCache >>= s.freeindex % 64

	return s
}

// 从中可以看出，heap_live在span的分配中更新。粒度是span级别的
// 什么时候会调用uncacheSpan？
// Return span from an MCache.
func (c *mcentral) uncacheSpan(s *mspan) {
	lock(&c.lock)

	s.incache = false

	if s.allocCount == 0 {
		throw("uncaching span but s.allocCount == 0")
	}

	cap := int32((s.npages << _PageShift) / s.elemsize)
	n := cap - int32(s.allocCount)
	if n > 0 {
		c.empty.remove(s)
		c.nonempty.insert(s)
		// mCentral_CacheSpan conservatively counted
		// unallocated slots in heap_live. Undo this.
		atomic.Xadd64(&memstats.heap_live, -int64(n)*int64(s.elemsize))
		// cacheSpan updated alloc assuming all objects on s
		// were going to be allocated. Adjust for any that
		// weren't.
		atomic.Xadd64(&c.nmalloc, -int64(n))
	}
	unlock(&c.lock)
}

// freeSpan在清扫s之后更新c和s
// 他设置s的sweepgen为最新的generation
// 基于s的空闲对象数目，把s移到c合适的list或者返回到heap
// 如果s归还到heap中，返回true
// 如果preserve为true，并不移动s(到heap)
// freeSpan updates c and s after sweeping s.
// It sets s's sweepgen to the latest generation,
// and, based on the number of free objects in s,
// moves s to the appropriate list of c or returns it
// to the heap.
// freeSpan returns true if s was returned to the heap.
// If preserve=true, it does not move s (the caller
// must take care of it).
func (c *mcentral) freeSpan(s *mspan, preserve bool, wasempty bool) bool {
	if s.incache {
		throw("freeSpan given cached span")
	}
	s.needzero = 1

	if preserve {
		// 只有从mcentral的cacehspan调用时才能设置preserve
		// 上面的sweep(true) 内部调用个函数，true会传递到这里
		// span必须在empty链表中
		// preserve is set only when called from MCentral_CacheSpan above,
		// the span must be in the empty list.
		if !s.inList() {
			throw("can't preserve unlinked span")
		}
		// 两个相等说明清理完毕
		atomic.Store(&s.sweepgen, mheap_.sweepgen)
		return false
	}

	lock(&c.lock)

	// Move to nonempty if necessary.
	if wasempty {
		c.empty.remove(s)
		c.nonempty.insert(s)
	}

	// 只到这里才更新sweepgen。这是span可以被mcache使用的信号，因此必须晚于
	// 上面的链表操作(实际上，只要晚于c的lock就好)
	// delay updating sweepgen until here. This is the signal that
	// the span may be used in an MCache, so it must come after the
	// linked list operations above (actually, just after the
	// lock of c above.)
	atomic.Store(&s.sweepgen, mheap_.sweepgen)

	if s.allocCount != 0 {
		unlock(&c.lock)
		return false
	}
	// 完全干净的 归还给heap
	c.nonempty.remove(s)
	unlock(&c.lock)
	mheap_.freeSpan(s, 0)
	return true
}

// grow allocates a new empty span from the heap and initializes it for c's size class.
// 首先调用mheap_.alloc函数向heap申请一个span；
// 然后将span里的连续page给切分成central对应的sizeclass的小内存块，
// 并将这些小内存串成链表挂在span的freelist上；最后将span放入到nonempty链表中。
// central在无空闲内存的时候，向heap只要了一个span，不是多个；
// 申请的span含多少page是根据central对应的sizeclass来确定。
func (c *mcentral) grow() *mspan {
	npages := uintptr(class_to_allocnpages[c.spanclass.sizeclass()])
	size := uintptr(class_to_size[c.spanclass.sizeclass()])
	n := (npages << _PageShift) / size

	// 从heap中分配一个 span，以页为单位
	s := mheap_.alloc(npages, c.spanclass, false, true)
	if s == nil {
		return nil
	}

	p := s.base()
	s.limit = p + size*n

	heapBitsForSpan(s.base()).initSpan(s)
	return s
}

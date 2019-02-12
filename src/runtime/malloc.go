// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Memory allocator.
//
// This was originally based on tcmalloc, but has diverged quite a bit.
// http://goog-perftools.sourceforge.net/doc/tcmalloc.html

// The main allocator works in runs of pages.
// Small allocation sizes (up to and including 32 kB) are
// rounded to one of about 70 size classes, each of which
// has its own free set of objects of exactly that size.
// Any free page of memory can be split into a set of objects
// of one size class, which are then managed using a free bitmap.
//
// The allocator's data structures are:
//
//	fixalloc: a free-list allocator for fixed-size off-heap objects,
//		used to manage storage used by the allocator.
//	mheap: the malloc heap, managed at page (8192-byte) granularity.
//	mspan: a run of pages managed by the mheap.
//	mcentral: collects all spans of a given size class.
//	mcache: a per-P cache of mspans with free space.
//	mstats: allocation statistics.
//
// Allocating a small object proceeds up a hierarchy of caches:
//
//	1. Round the size up to one of the small size classes
//	   and look in the corresponding mspan in this P's mcache.
//	   Scan the mspan's free bitmap to find a free slot.
//	   If there is a free slot, allocate it.
//	   This can all be done without acquiring a lock.
//
//	2. If the mspan has no free slots, obtain a new mspan
//	   from the mcentral's list of mspans of the required size
//	   class that have free space.
//	   Obtaining a whole span amortizes the cost of locking
//	   the mcentral.
//
//	3. If the mcentral's mspan list is empty, obtain a run
//	   of pages from the mheap to use for the mspan.
//
//	4. If the mheap is empty or has no page runs large enough,
//	   allocate a new group of pages (at least 1MB) from the
//	   operating system. Allocating a large run of pages
//	   amortizes the cost of talking to the operating system.
//
// Sweeping an mspan and freeing objects on it proceeds up a similar
// hierarchy:
//
//	1. If the mspan is being swept in response to allocation, it
//	   is returned to the mcache to satisfy the allocation.
//
//	2. Otherwise, if the mspan still has allocated objects in it,
//	   it is placed on the mcentral free list for the mspan's size
//	   class.
//
//	3. Otherwise, if all objects in the mspan are free, the mspan
//	   is now "idle", so it is returned to the mheap and no longer
//	   has a size class.
//	   This may coalesce it with adjacent idle mspans.
//
//	4. If an mspan remains idle for long enough, return its pages
//	   to the operating system.
//
// Allocating and freeing a large object uses the mheap
// directly, bypassing the mcache and mcentral.
//
// Free object slots in an mspan are zeroed only if mspan.needzero is
// false. If needzero is true, objects are zeroed as they are
// allocated. There are various benefits to delaying zeroing this way:
//
//	1. Stack frame allocation can avoid zeroing altogether.
//
//	2. It exhibits better temporal locality, since the program is
//	   probably about to write to the memory.
//
//	3. We don't zero pages that never get reused.
//
// 内存分配器.
// 这最初是以 tmalloc 为基础的, 但已经有了不少改变。
// 主分配器在页面运行中起作用。小的分配大小（高达并包括32 kB）被舍入到大约70个大小类中的一个，每个大小类都有自己的一组完全相同大小的对象。
// 任何可用的内存页面都可以拆分为一个大小类的对象集，然后使用空闲位图进行管理。分配器的数据结构是：
// fixalloc：
// 固定尺寸的堆对象空闲列表分配器，用来管理分配器的存储。
// mheap：
// 堆分配器，以页面（8192字节）粒度管理。
// mspan：
// 由mheap管理的一系列页面。
// mcentral：
// 所有给定大小类的mspan集合，Central组件其实也是一个缓存，但它缓存的不是小对象内存块，而是一组一组的内存page(一个page占4k大小)
// mcache：
// 每个P上的缓存，具有一定的内存空间。
// mstats：
// 分配统计。

// 分配一个小对象会进入缓存层次结构：
// 1.将大小调整为一个小型类，并查看此P的mcache中相应的mspan。扫描mspan的免费位图以找到空闲插槽。如果有空闲插槽，请分配它。这可以在不获取锁定的情况下完成。
// 2.如果mspan没有空闲槽，则从mcentral所需大小类的mspans列表中获取一个具有可用空间的新mspan。获得整个跨度可以分摊锁定中心的成本。
// 3.如果mcentral的mspan列表为空，则从mheap获取一组页面以用于mspan。
// 4.如果mheap为空或没有足够大的页面运行，请从操作系统分配一组新页面（至少1MB）。分配大量页面会分摊与操作系统通信的成本。

// 扫描mspan并在其上释放对象会产生类似的层次结构：
// 1.如果mspan在响应分配时被扫描，则返回到mcache以满足分配。
// 2.否则，如果mspan仍然在其中分配了对象，则将其放置在mspan的大小类的mcentral空闲列表中。
// 3.否则，如果mspan中的所有对象都是空闲的，则mspan现在处于“空闲”状态，因此它将返回到mheap并且不再具有size类。这可以将其与相邻的空闲mspans合并。
// 4.如果mspan保持空闲的时间足够长，则将其页面返回到操作系统。

// 分配和释放大对象直接使用mheap，绕过mcache和mcentral。
// 仅当mspan.needzero为false时，mspan中的自由对象槽才会归零。如果needzero为true，则对象在分配时归零。以这种方式延迟归零有很多好处：
// 1.堆栈帧分配可以完全避免归零。
// 2.它表现出更好的时间局部性，因为程序可能要写入内存。
// 3.我们不会零页面永远不会被重复使用。
package runtime

import (
	"runtime/internal/sys"
	"unsafe"
)

const (
	debugMalloc = false

	maxTinySize   = _TinySize
	tinySizeClass = _TinySizeClass
	maxSmallSize  = _MaxSmallSize

	pageShift = _PageShift
	pageSize  = _PageSize
	pageMask  = _PageMask
	// By construction, single page spans of the smallest object class
	// have the most objects per span.
	maxObjsPerSpan = pageSize / 8

	mSpanInUse = _MSpanInUse

	concurrentSweep = _ConcurrentSweep

	// glang内存页的大小，8K
	_PageSize = 1 << _PageShift
	_PageMask = _PageSize - 1

	// _64bit = 1 on 64-bit systems, 0 on 32-bit systems
	_64bit = 1 << (^uintptr(0) >> 63) / 2

	// Tiny allocator parameters, see "Tiny allocator" comment in malloc.go.
	_TinySize      = 16
	_TinySizeClass = int8(2)

	_FixAllocChunk  = 16 << 10               // Chunk size for FixAlloc
	_MaxMHeapList   = 1 << (20 - _PageShift) // Maximum page length for fixed-size list in MHeap.
	_HeapAllocChunk = 1 << 20                // Chunk size for heap growth

	// Per-P, per order stack segment cache size.
	_StackCacheSize = 32 * 1024

	// Number of orders that get caching. Order 0 is FixedStack
	// and each successive order is twice as large.
	// We want to cache 2KB, 4KB, 8KB, and 16KB stacks. Larger stacks
	// will be allocated directly.
	// Since FixedStack is different on different systems, we
	// must vary NumStackOrders to keep the same maximum cached size.
	//   OS               | FixedStack | NumStackOrders
	//   -----------------+------------+---------------
	//   linux/darwin/bsd | 2KB        | 4
	//   windows/32       | 4KB        | 3
	//   windows/64       | 8KB        | 2
	//   plan9            | 4KB        | 3
	_NumStackOrders = 4 - sys.PtrSize/4*sys.GoosWindows - 1*sys.GoosPlan9

	// Number of bits in page to span calculations (4k pages).
	// On Windows 64-bit we limit the arena to 32GB or 35 bits.
	// Windows counts memory used by page table into committed memory
	// of the process, so we can't reserve too much memory.
	// See https://golang.org/issue/5402 and https://golang.org/issue/5236.
	// On other 64-bit platforms, we limit the arena to 512GB, or 39 bits.
	// On 32-bit, we don't bother limiting anything, so we use the full 32-bit address.
	// The only exception is mips32 which only has access to low 2GB of virtual memory.
	// On Darwin/arm64, we cannot reserve more than ~5GB of virtual memory,
	// but as most devices have less than 4GB of physical memory anyway, we
	// try to be conservative here, and only ask for a 2GB heap.
	_MHeapMap_TotalBits = (_64bit*sys.GoosWindows)*35 + (_64bit*(1-sys.GoosWindows)*(1-sys.GoosDarwin*sys.GoarchArm64))*39 + sys.GoosDarwin*sys.GoarchArm64*31 + (1-_64bit)*(32-(sys.GoarchMips+sys.GoarchMipsle))
	_MHeapMap_Bits      = _MHeapMap_TotalBits - _PageShift

	// _MaxMem is the maximum heap arena size minus 1.
	//
	// On 32-bit, this is also the maximum heap pointer value,
	// since the arena starts at address 0.
	_MaxMem = 1<<_MHeapMap_TotalBits - 1

	// Max number of threads to run garbage collection.
	// 2, 3, and 4 are all plausible maximums depending
	// on the hardware details of the machine. The garbage
	// collector scales well to 32 cpus.
	_MaxGcproc = 32

	// minLegalPointer is the smallest possible legal pointer.
	// This is the smallest possible architectural page size,
	// since we assume that the first page is never mapped.
	//
	// This should agree with minZeroPage in the compiler.
	minLegalPointer uintptr = 4096
)

// physPageSize is the size in bytes of the OS's physical pages.
// Mapping and unmapping operations must be done at multiples of
// physPageSize.
//
// This must be set by the OS init code (typically in osinit) before
// mallocinit.
var physPageSize uintptr

// OS-defined helpers:
//
// sysAlloc obtains a large chunk of zeroed memory from the
// operating system, typically on the order of a hundred kilobytes
// or a megabyte.
// NOTE: sysAlloc returns OS-aligned memory, but the heap allocator
// may use larger alignment, so the caller must be careful to realign the
// memory obtained by sysAlloc.
//
// SysUnused notifies the operating system that the contents
// of the memory region are no longer needed and can be reused
// for other purposes.
// SysUsed notifies the operating system that the contents
// of the memory region are needed again.
//
// SysFree returns it unconditionally; this is only used if
// an out-of-memory error has been detected midway through
// an allocation. It is okay if SysFree is a no-op.
//
// SysReserve reserves address space without allocating memory.
// If the pointer passed to it is non-nil, the caller wants the
// reservation there, but SysReserve can still choose another
// location if that one is unavailable. On some systems and in some
// cases SysReserve will simply check that the address space is
// available and not actually reserve it. If SysReserve returns
// non-nil, it sets *reserved to true if the address space is
// reserved, false if it has merely been checked.
// NOTE: SysReserve returns OS-aligned memory, but the heap allocator
// may use larger alignment, so the caler must be careful to realign the
// memory obtained by sysAlloc.
//
// SysMap maps previously reserved address space for use.
// The reserved argument is true if the address space was really
// reserved, not merely checked.
//
// SysFault marks a (already sysAlloc'd) region to fault
// if accessed. Used only for debugging the runtime.
//
// 定义系统的一些帮助函数:
// sysAlloc 从os获得一大块内存（0初始化），大约100kb或者1mb的样子
// note：sysAlloc返回的内存是os对齐的，但是heap allocator会用更大的对齐
// 因此调用者必须小心对内存重新对齐
// os一般是4k，go是8k
//
// sysUnuesd 通知os这段内存的内容已经不在需要了，可以复用于其他用途(os不一定会回收这个页)
// sysUsed 通知os这块内存的内容再次需要使用了。
// 这样做可以做点优化，如果调用sysUnused之后很快调用sysUesd，os不用进行页回收和页分配
// sysFree 无条件的归还内存(给os)，只有malloc中途发生out of memory的时候才会调用，
// sysFree 什么都不做也是可以的。(oom问题崩掉就好了)
//
// sysReserve 预留一段内存(未分配),如果参数非空，说么调用者希望从这里开始预留，
// 但是sysReserve 仍然可以选择另一个位置如果希望的位置不可用，
// 有些os上某些情况 sysReserve 仅仅检查位置是否可用而并不真正的预留它，
// 当sysReserve返回非空时，如果这段内存预留了，它会设置 *reserved为true；
// 否则设置为false，这意味着仅仅做了检查，没有预留
// 注意：SysReserve返回OS对齐的内存，但堆分配器可能使用更大的对齐，
// 因此编码器必须小心重新调整sysAlloc获取的内存。
//
// sysMap负责映射这段预留的内存(预留操作只是告诉os我要用了，os不一定真的给)，
// reserved 参数意思同上
//
// sysFault 标记一段内存为fault(已经分配的内存)，仅用于调试

// 由schedinit调用，进程启动后会进入这里
// mallocinit 实现内存分配的一些初始化，检查对象规格大小对照表，
// 还将连续的虚拟地址，划分成三大块：
/*
      512MB      16GB            512GB
	+-------+-------------+-------------------+
	| spans |    bitmap   |       arena       |
	+-------+-------------+-------------------+
*/
// 这三个区域可以按需同步线性扩张，无须预分配内存。
func mallocinit() {
	if class_to_size[_TinySizeClass] != _TinySize {
		throw("bad TinySizeClass")
	}

	testdefersizes()

	// Copy class sizes out for statistics table.
	for i := range class_to_size {
		memstats.by_size[i].size = uint32(class_to_size[i])
	}

	// Check physPageSize.
	if physPageSize == 0 {
		// The OS init code failed to fetch the physical page size.
		throw("failed to get system page size")
	}
	if physPageSize < minPhysPageSize {
		print("system page size (", physPageSize, ") is smaller than minimum page size (", minPhysPageSize, ")\n")
		throw("bad system page size")
	}
	if physPageSize&(physPageSize-1) != 0 {
		print("system page size (", physPageSize, ") must be a power of 2\n")
		throw("bad system page size")
	}

	// The auxiliary regions start at p and are laid out in the
	// following order: spans, bitmap, arena.
	var p, pSize uintptr
	var reserved bool
	// _MaxMem在linux x64下是512g-1，spanSize=512G/8k * 8=512M
	// 计算span大小
	// The spans array holds one *mspan per _PageSize of arena.
	var spansSize uintptr = (_MaxMem + 1) / _PageSize * sys.PtrSize
	spansSize = round(spansSize, _PageSize)
	// The bitmap holds 2 bits per word of arena.
	// bitmapSize=512G/(8*8/2)=16GB
	var bitmapSize uintptr = (_MaxMem + 1) / (sys.PtrSize * 8 / 2)
	bitmapSize = round(bitmapSize, _PageSize)

	// Set up the allocation arena, a contiguous area of memory where
	// allocated data will be found.
	// 设置arena区域，分配的数据在一段连续内存中？？？
	if sys.PtrSize == 8 {
		// 64位机器，从一段连续的预留空间分配
		// 512g 足够了
		// On a 64-bit machine, allocate from a single contiguous reservation.
		// 512 GB (MaxMem) should be big enough for now.
		//
		// The code will work with the reservation at any address, but ask
		// SysReserve to use 0x0000XXc000000000 if possible (XX=00...7f).
		// Allocating a 512 GB region takes away 39 bits, and the amd64
		// doesn't let us choose the top 17 bits, so that leaves the 9 bits
		// in the middle of 0x00c0 for us to choose. Choosing 0x00c0 means
		// that the valid memory addresses will begin 0x00c0, 0x00c1, ..., 0x00df.
		// In little-endian, that's c0 00, c1 00, ..., df 00. None of those are valid
		// UTF-8 sequences, and they are otherwise as far away from
		// ff (likely a common byte) as possible. If that fails, we try other 0xXXc0
		// addresses. An earlier attempt to use 0x11f8 caused out of memory errors
		// on OS X during thread allocations.  0x00c0 causes conflicts with
		// AddressSanitizer which reserves all memory up to 0x0100.
		// These choices are both for debuggability and to reduce the
		// odds of a conservative garbage collector (as is still used in gccgo)
		// not collecting memory because some non-pointer block of memory
		// had a bit pattern that matched a memory address.
		//
		// Actually we reserve 544 GB (because the bitmap ends up being 32 GB)
		// but it hardly matters: e0 00 is not valid UTF-8 either.
		//
		// If this fails we fall back to the 32 bit memory mechanism
		//
		// However, on arm64, we ignore all this advice above and slam the
		// allocation at 0x40 << 32 because when using 4k pages with 3-level
		// translation buffers, the user address space is limited to 39 bits
		// On darwin/arm64, the address space is even smaller.
		//
		arenaSize := round(_MaxMem, _PageSize)
		// 计算整个区域的大小
		pSize = bitmapSize + spansSize + arenaSize + _PageSize
		// 尝试从 0x1c000000000 开始设置保留地址
		// 如果失败，则尝试 0x1c000000000～0x7fc000000000
		for i := 0; i <= 0x7f; i++ {
			switch {
			case GOARCH == "arm64" && GOOS == "darwin":
				p = uintptr(i)<<40 | uintptrMask&(0x0013<<28)
			case GOARCH == "arm64":
				p = uintptr(i)<<40 | uintptrMask&(0x0040<<32)
			default:
				p = uintptr(i)<<40 | uintptrMask&(0x00c0<<32)
			}
			// 申请一大块内存地址保留区，后续所有page的申请都会从这个地址区里分, in mem_linux.go
			p = uintptr(sysReserve(unsafe.Pointer(p), pSize, &reserved))
			if p != 0 {
				break
			}
		}
	}

	if p == 0 {
		// On a 32-bit machine, we can't typically get away
		// with a giant virtual address space reservation.
		// Instead we map the memory information bitmap
		// immediately after the data segment, large enough
		// to handle the entire 4GB address space (256 MB),
		// along with a reservation for an initial arena.
		// When that gets used up, we'll start asking the kernel
		// for any memory anywhere.

		// We want to start the arena low, but if we're linked
		// against C code, it's possible global constructors
		// have called malloc and adjusted the process' brk.
		// Query the brk so we can avoid trying to map the
		// arena over it (which will cause the kernel to put
		// the arena somewhere else, likely at a high
		// address).
		procBrk := sbrk0()

		// If we fail to allocate, try again with a smaller arena.
		// This is necessary on Android L where we share a process
		// with ART, which reserves virtual memory aggressively.
		// In the worst case, fall back to a 0-sized initial arena,
		// in the hope that subsequent reservations will succeed.
		arenaSizes := []uintptr{
			512 << 20,
			256 << 20,
			128 << 20,
			0,
		}

		for _, arenaSize := range arenaSizes {
			// SysReserve treats the address we ask for, end, as a hint,
			// not as an absolute requirement. If we ask for the end
			// of the data segment but the operating system requires
			// a little more space before we can start allocating, it will
			// give out a slightly higher pointer. Except QEMU, which
			// is buggy, as usual: it won't adjust the pointer upward.
			// So adjust it upward a little bit ourselves: 1/4 MB to get
			// away from the running binary image and then round up
			// to a MB boundary.
			p = round(firstmoduledata.end+(1<<18), 1<<20)
			pSize = bitmapSize + spansSize + arenaSize + _PageSize
			if p <= procBrk && procBrk < p+pSize {
				// Move the start above the brk,
				// leaving some room for future brk
				// expansion.
				p = round(procBrk+(1<<20), 1<<20)
			}
			p = uintptr(sysReserve(unsafe.Pointer(p), pSize, &reserved))
			if p != 0 {
				break
			}
		}
		if p == 0 {
			throw("runtime: cannot reserve arena virtual address space")
		}
	}

	// 页大小可能比os的页大小大（go的是8k，linux的是4k，需要对齐）
	// PageSize can be larger than OS definition of page size,
	// so SysReserve can give us a PageSize-unaligned pointer.
	// To overcome this we ask for PageSize more and round up the pointer.
	p1 := round(p, _PageSize) // 对齐，这样可能有一个页无法使用（对齐的代价）
	pSize -= p1 - p           // 减去对齐产生的边角料
	// p1 - - ->
	// spans | bitMap | arena |
	// 这里只是确定了基本布局，里面的内存还都没分配，也没有预留
	spansStart := p1
	p1 += spansSize
	mheap_.bitmap = p1 + bitmapSize
	p1 += bitmapSize
	if sys.PtrSize == 4 {
		// Set arena_start such that we can accept memory
		// reservations located anywhere in the 4GB virtual space.
		mheap_.arena_start = 0
	} else {
		mheap_.arena_start = p1
	}
	// 初始化好heap的arena以及bitmap
	mheap_.arena_end = p + pSize
	mheap_.arena_used = p1
	mheap_.arena_alloc = p1
	mheap_.arena_reserved = reserved

	if mheap_.arena_start&(_PageSize-1) != 0 {
		println("bad pagesize", hex(p), hex(p1), hex(spansSize), hex(bitmapSize), hex(_PageSize), "start", hex(mheap_.arena_start))
		throw("misrounded allocation in mallocinit")
	}

	// Initialize the rest of the allocator.
	// 堆分配器的其他属性初始化
	mheap_.init(spansStart, spansSize)
	_g_ := getg()
	// 主线程分配mcache，其他线程是通过 proc.go: procresize 函数赋值的。
	_g_.m.mcache = allocmcache()
}

// sysAlloc allocates the next n bytes from the heap arena. The
// returned pointer is always _PageSize aligned and between
// h.arena_start and h.arena_end. sysAlloc returns nil on failure.
// There is no corresponding free function.
// sysAlloc 从heap arena区域分配n bytes的内存。
// 返回值总是 _PageSize 对齐的(8k对齐)且处于arena_start和arena_end之间
// 发生错误返回nil
// 没有对应的free函数
// 大于32k的对象会走这里，span分配最终也是走这里
func (h *mheap) sysAlloc(n uintptr) unsafe.Pointer {
	// strandLimit 是当前arena块的最大bytes数。如果我们需要更多的内存，
	// 我们回到sysAlloc就可以了
	// 这里的strand from应该是断层的意思，有一部分边角料由于对齐的原因无法使用
	// 这块边角料不超过16M就可以，太大了可能会导致太浪费了把。
	// strandLimit is the maximum number of bytes to strand from
	// the current arena block. If we would need to strand more
	// than this, we fall back to sysAlloc'ing just enough for
	// this allocation.
	const strandLimit = 16 << 20 //16M

	// 需要进行预留
	if n > h.arena_end-h.arena_alloc {
		// 如果arena区域的大小还不到_MaxMem，试着预留更多的内存
		// If we haven't grown the arena to _MaxMem yet, try
		// to reserve some more address space.
		p_size := round(n+_PageSize, 256<<20)
		new_end := h.arena_end + p_size // Careful: can overflow
		if h.arena_end <= new_end && new_end-h.arena_start-1 <= _MaxMem {
			// TODO: It would be bad if part of the arena
			// is reserved and part is not.
			// TODO: 如果一段内存一部分预留了一部分没有预留，会发生错误
			var reserved bool
			p := uintptr(sysReserve(unsafe.Pointer(h.arena_end), p_size, &reserved))
			if p == 0 {
				// TODO: Try smaller reservation
				// growths in case we're in a crowded
				// 32-bit address space.
				// 出错，64位直接返回nil(一般都是代码写错了)
				goto reservationFailed
			}
			// p can be just about anywhere in the address
			// space, including before arena_end.
			// p可以在任何地址，包括arena_end之前
			if p == h.arena_end {
				// The new block is contiguous with
				// the current block. Extend the
				// current arena block.
				// 新的块和当前的块相邻。拓展当前块
				h.arena_end = new_end
				h.arena_reserved = reserved
			} else if h.arena_start <= p && p+p_size-h.arena_start-1 <= _MaxMem && h.arena_end-h.arena_alloc < strandLimit {
				// We were able to reserve more memory
				// within the arena space, but it's
				// not contiguous with our previous
				// reservation. It could be before or
				// after our current arena_used.
				//
				// Keep everything page-aligned.
				// Our pages are bigger than hardware pages.
				// p在arena_start之后且内存不会溢出且空隙不超过16M
				// 我们可以在arena区域预留更大的内存，
				// 但是这和之前的预留不连续
				// 可能在arena_used前或者后
				//
				// 保证一切都是page-aligned的(即8k对齐)
				// go的page比os的大
				// 由于地址不连续，所以是p+p_size，
				// 上面的其实是p=arena_end的特殊情况
				// 然后对齐，接着把alloc设置为p
				// 分配内存是在 [arena_alloc, arena_end)中
				// 之前的已经不会在使用了(影响不大，cow不w就没什么开销)
				h.arena_end = p + p_size
				p = round(p, _PageSize)
				h.arena_alloc = p
				h.arena_reserved = reserved
			} else {
				// 1) It's not in the arena, so we
				// can't use it. (This should never
				// happen on 32-bit.)
				//
				// 2) We would need to discard too
				// much of our current arena block to
				// use it.
				//
				// We haven't added this allocation to
				// the stats, so subtract it from a
				// fake stat (but avoid underflow).
				//
				// We'll fall back to a small sysAlloc.
				// 我们处于以下其中一种情况
				//
				// 1) 不在arena中，无法使用，(32位os不会发生)
				// We got a mapping, but either
				//
				// 2) 我们可能需要废弃很大一块区域来使用它
				// (对齐的代价或者其他原因)
				//
				// 我们还没有统计这次分配，因此去掉它防止下溢
				stat := uint64(p_size)
				// 释放这段内存
				sysFree(unsafe.Pointer(p), p_size, &stat)
			}
		}
		// 其他情况什么都不做
	}

	if n <= h.arena_end-h.arena_alloc {
		// Keep taking from our reservation.
		p := h.arena_alloc
		sysMap(unsafe.Pointer(p), n, h.arena_reserved, &memstats.heap_sys)
		h.arena_alloc += n
		if h.arena_alloc > h.arena_used {
			h.setArenaUsed(h.arena_alloc, true)
		}

		if p&(_PageSize-1) != 0 {
			throw("misrounded allocation in MHeap_SysAlloc")
		}
		return unsafe.Pointer(p)
	}

reservationFailed:
	// If using 64-bit, our reservation is all we have.
	if sys.PtrSize != 4 {
		return nil
	}

	// On 32-bit, once the reservation is gone we can
	// try to get memory at a location chosen by the OS.
	p_size := round(n, _PageSize) + _PageSize
	p := uintptr(sysAlloc(p_size, &memstats.heap_sys))
	if p == 0 {
		return nil
	}

	if p < h.arena_start || p+p_size-h.arena_start > _MaxMem {
		// This shouldn't be possible because _MaxMem is the
		// whole address space on 32-bit.
		top := uint64(h.arena_start) + _MaxMem
		print("runtime: memory allocated by OS (", hex(p), ") not in usable range [", hex(h.arena_start), ",", hex(top), ")\n")
		sysFree(unsafe.Pointer(p), p_size, &memstats.heap_sys)
		return nil
	}

	p += -p & (_PageSize - 1)
	if p+n > h.arena_used {
		h.setArenaUsed(p+n, true)
	}

	if p&(_PageSize-1) != 0 {
		throw("misrounded allocation in MHeap_SysAlloc")
	}
	return unsafe.Pointer(p)
}

// base address for all 0-byte allocations
var zerobase uintptr

// nextFreeFast returns the next free object if one is quickly available.
// Otherwise it returns 0.
// 初始情况allocCache都是^uint64(0)，freeindex是1
// 然后freeidx=2 allocCache更新为^uint64(0)>>1
// 如果内存没有释放，theBit一直都是0
func nextFreeFast(s *mspan) gclinkptr {
	// 第几位开始不是0
	theBit := sys.Ctz64(s.allocCache) // Is there a free object in the allocCache?
	// 超过了说明没可用的了
	if theBit < 64 {
		result := s.freeindex + uintptr(theBit)
		// 超过了表明无可用的
		if result < s.nelems {
			// 可能有可用的
			freeidx := result + 1
			if freeidx%64 == 0 && freeidx != s.nelems {
				// 不是最后一个，且整除64，返回0
				return 0
			}
			// 真的有可用的
			// 更新一下
			s.allocCache >>= uint(theBit + 1)
			s.freeindex = freeidx
			s.allocCount++
			return gclinkptr(result*s.elemsize + s.base())
		}
	}
	return 0
}

// nextFree returns the next free object from the cached span if one is available.
// Otherwise it refills the cache with a span with an available object and
// returns that object along with a flag indicating that this was a heavy
// weight allocation. If it is a heavy weight allocation the caller must
// determine whether a new GC cycle needs to be started or if the GC is active
// whether this goroutine needs to assist the GC.
// 找到freeIndex，如果span里面所有元素都已分配, 则需要分配新的span
func (c *mcache) nextFree(spc spanClass) (v gclinkptr, s *mspan, shouldhelpgc bool) {
	s = c.alloc[spc]
	shouldhelpgc = false
	freeIndex := s.nextFreeIndex()
	if freeIndex == s.nelems {
		// The span is full.
		if uintptr(s.allocCount) != s.nelems {
			println("runtime: s.allocCount=", s.allocCount, "s.nelems=", s.nelems)
			throw("s.allocCount != s.nelems && freeIndex == s.nelems")
		}
		systemstack(func() {
			// 获取一个spc规格的span
			c.refill(spc)
		})
		shouldhelpgc = true
		s = c.alloc[spc]

		freeIndex = s.nextFreeIndex()
	}

	if freeIndex >= s.nelems {
		throw("freeIndex is not valid")
	}

	v = gclinkptr(freeIndex*s.elemsize + s.base())
	s.allocCount++
	if uintptr(s.allocCount) > s.nelems {
		println("s.allocCount=", s.allocCount, "s.nelems=", s.nelems)
		throw("s.allocCount > s.nelems")
	}
	return
}

// Allocate an object of size bytes.
// Small objects are allocated from the per-P cache's free lists.
// Large objects (> 32 kB) are allocated straight from the heap.
// mallocgc是从堆中分配对象，并根据情况触发gc
// 分配内存的大概流程
// 1. 计算待分配对象对应的规格
// 2. 从 cache.alloc 数组找到规格相同的 span
// 3. 从 span.freelist 链表提取可用的 object
// 4. 如果 span.freelist 为空，从 central 获取新的 span
// 5. 如果 central.nonempty 为空，从 heap.free/freelarge 获取，并切分成 object 链。
// 6. 如 heap 没有大小合适闲置的 span，向操作系统申请新的内存
func mallocgc(size uintptr, typ *_type, needzero bool) unsafe.Pointer {
	// 如果是 _GCmarktermination 阶段，那么抛出异常
	if gcphase == _GCmarktermination {
		throw("mallocgc called with gcphase == _GCmarktermination")
	}

	if size == 0 {
		return unsafe.Pointer(&zerobase)
	}

	// 绕过内存分配器（和GC）
	if debug.sbrk != 0 {
		align := uintptr(16)
		if typ != nil {
			align = uintptr(typ.align)
		}
		return persistentalloc(size, align, &memstats.other_sys)
	}

	// assistG is the G to charge for this allocation, or nil if
	// GC is not currently active.
	// 判断是否要辅助GC工作
	// gcBlackenEnabled在GC的标记阶段会开启
	var assistG *g
	if gcBlackenEnabled != 0 {
		// Charge the current user G for this allocation.
		assistG = getg()
		if assistG.m.curg != nil {
			assistG = assistG.m.curg
		}
		// Charge the allocation against the G. We'll account
		// for internal fragmentation at the end of mallocgc.
		assistG.gcAssistBytes -= int64(size)

		if assistG.gcAssistBytes < 0 {
			// This G is in debt. Assist the GC to correct
			// this before allocating. This must happen
			// before disabling preemption.
			gcAssistAlloc(assistG)
		}
	}

	// Set mp.mallocing to keep from being preempted by GC.
	mp := acquirem()
	if mp.mallocing != 0 {
		throw("malloc deadlock")
	}
	if mp.gsignal == getg() {
		throw("malloc during signal")
	}
	// 标志正在分配
	mp.mallocing = 1

	shouldhelpgc := false
	dataSize := size
	// 获取当前M的mcache
	c := gomcache()
	var x unsafe.Pointer
	// 如果类型是nil或者不带指针，就不需要扫描
	noscan := typ == nil || typ.kind&kindNoPointers != 0
	// size <= 32k
	if size <= maxSmallSize { // 分配小对象
		// 没指针且 size 小于16，用微小分配器分配内存
		if noscan && size < maxTinySize {
			// Tiny allocator.
			//
			// Tiny allocator combines several tiny allocation requests
			// into a single memory block. The resulting memory block
			// is freed when all subobjects are unreachable. The subobjects
			// must be noscan (don't have pointers), this ensures that
			// the amount of potentially wasted memory is bounded.
			//
			// Size of the memory block used for combining (maxTinySize) is tunable.
			// Current setting is 16 bytes, which relates to 2x worst case memory
			// wastage (when all but one subobjects are unreachable).
			// 8 bytes would result in no wastage at all, but provides less
			// opportunities for combining.
			// 32 bytes provides more opportunities for combining,
			// but can lead to 4x worst case wastage.
			// The best case winning is 8x regardless of block size.
			//
			// Objects obtained from tiny allocator must not be freed explicitly.
			// So when an object will be freed explicitly, we ensure that
			// its size >= maxTinySize.
			//
			// SetFinalizer has a special case for objects potentially coming
			// from tiny allocator, it such case it allows to set finalizers
			// for an inner byte of a memory block.
			//
			// The main targets of tiny allocator are small strings and
			// standalone escaping variables. On a json benchmark
			// the allocator reduces number of allocations by ~12% and
			// reduces heap size by ~20%.
			//
			// 微小的分配器。
			// 微型分配器将几个微小的分配请求组合到一个内存块中。当所有子对象都无法访问时，将释放生成的内存块。
			// 子对象必须是noscan（没有指针），这可以确保可能浪费的内存量受到限制。
			//
			// 用于组合的内存块的大小（maxTinySize）是可调的。当前设置为16个字节，
			// 这与2x最坏情况的内存浪费（当除了一个子对象之外的所有子对象都无法访问时）有关。
			// 8字节将导致完全没有浪费，但提供更少的组合机会。 32字节提供了更多的组合机会，
			// 但可能导致4倍最坏情况下的浪费。无论块大小如何，获胜的最佳案例是8倍。
			//
			// 从微小分配器获得的对象不得明确释放。因此，当显式释放对象时，我们确保其大小> = maxTinySize。
			//
			// SetFinalizer对于可能来自微小分配器的对象有一个特殊情况，它允许为内存块的内部字节设置终结器。
			//
			// 微分配器的主要目标是小字符串和独立的转义变量。在json基准测试中，分配器将分配数量减少了大约12％，并将堆大小减少了大约20％。
			off := c.tinyoffset
			// Align tiny pointer for required (conservative) alignment.
			// 对齐，调整偏移量
			if size&7 == 0 {
				off = round(off, 8)
			} else if size&3 == 0 {
				off = round(off, 4)
			} else if size&1 == 0 {
				off = round(off, 2)
			}
			// 如果剩余空间足够
			if off+size <= maxTinySize && c.tiny != 0 {
				// The object fits into existing tiny block.
				// 返回对象的指针，切调整偏移量为下次分配做准备
				x = unsafe.Pointer(c.tiny + off)
				c.tinyoffset = off + size
				c.local_tinyallocs++
				mp.mallocing = 0
				releasem(mp)
				return x
			}
			// Allocate a new maxTinySize block.
			// 如果空间不够，自然需要获取新的 maxTinySize 块。
			span := c.alloc[tinySpanClass]
			// 尝试快速的从这个span中分配
			v := nextFreeFast(span)
			if v == 0 {
				// 如果从上面的span中没有可用的 object， 那么从 central 中获取。
				v, _, shouldhelpgc = c.nextFree(tinySpanClass)
			}
			x = unsafe.Pointer(v)
			(*[2]uint64)(x)[0] = 0
			(*[2]uint64)(x)[1] = 0
			// See if we need to replace the existing tiny block with the new one
			// based on amount of remaining free space.
			if size < c.tinyoffset || c.tiny == 0 {
				c.tiny = uintptr(x)
				c.tinyoffset = size
			}
			size = maxTinySize
		} else {
			// 普通小对象（>16 & <32k）
			// 查表确定 sizeclass
			var sizeclass uint8
			if size <= smallSizeMax-8 {
				sizeclass = size_to_class8[(size+smallSizeDiv-1)/smallSizeDiv]
			} else {
				sizeclass = size_to_class128[(size-smallSizeMax+largeSizeDiv-1)/largeSizeDiv]
			}
			size = uintptr(class_to_size[sizeclass])
			spc := makeSpanClass(sizeclass, noscan)
			span := c.alloc[spc]
			// 尝试快速的从这个span中分配
			v := nextFreeFast(span)
			if v == 0 {
				v, span, shouldhelpgc = c.nextFree(spc)
			}
			x = unsafe.Pointer(v)
			// 如果需要清零，就调用memclrNoHeapPointers将对象初始化为0
			if needzero && span.needzero != 0 {
				memclrNoHeapPointers(unsafe.Pointer(v), size)
			}
		}
	} else { // 分配大对象
		// 大对象直接从 heap 中分配
		var s *mspan
		shouldhelpgc = true
		systemstack(func() {
			s = largeAlloc(size, needzero, noscan)
		})
		s.freeindex = 1
		s.allocCount = 1
		x = unsafe.Pointer(s.base())
		size = s.elemsize
	}

	var scanSize uintptr
	if !noscan {
		// If allocating a defer+arg block, now that we've picked a malloc size
		// large enough to hold everything, cut the "asked for" size down to
		// just the defer header, so that the GC bitmap will record the arg block
		// as containing nothing at all (as if it were unused space at the end of
		// a malloc block caused by size rounding).
		// The defer arg areas are scanned as part of scanstack.
		if typ == deferType {
			dataSize = unsafe.Sizeof(_defer{})
		}
		heapBitsSetType(uintptr(x), size, dataSize, typ)
		if dataSize > typ.size {
			// Array allocation. If there are any
			// pointers, GC has to scan to the last
			// element.
			if typ.ptrdata != 0 {
				scanSize = dataSize - typ.size + typ.ptrdata
			}
		} else {
			scanSize = typ.ptrdata
		}
		c.local_scan += scanSize
	}

	// Ensure that the stores above that initialize x to
	// type-safe memory and set the heap bits occur before
	// the caller can make x observable to the garbage
	// collector. Otherwise, on weakly ordered machines,
	// the garbage collector could follow a pointer to x,
	// but see uninitialized memory or stale heap bits.
	publicationBarrier()

	// Allocate black during GC.
	// All slots hold nil so no scanning is needed.
	// This may be racing with GC so do it atomically if there can be
	// a race marking the bit.
	// 如果gc阶段不是_GCoff，也就是说正在gc
	// 直接标记为黑色对象，黑色对象不会被回收
	if gcphase != _GCoff {
		gcmarknewobject(uintptr(x), size, scanSize)
	}

	if raceenabled {
		racemalloc(x, size)
	}

	// -msan
	if msanenabled {
		msanmalloc(x, size)
	}

	mp.mallocing = 0
	releasem(mp)

	if debug.allocfreetrace != 0 {
		tracealloc(x, size, typ)
	}

	if rate := MemProfileRate; rate > 0 {
		if size < uintptr(rate) && int32(size) < c.next_sample {
			c.next_sample -= int32(size)
		} else {
			mp := acquirem()
			profilealloc(mp, x, size)
			releasem(mp)
		}
	}

	if assistG != nil {
		// Account for internal fragmentation in the assist
		// debt now that we know it.
		assistG.gcAssistBytes -= int64(size - dataSize)
	}

	// 如果之前分配了新的span, 则判断是否需要后台启动GC
	if shouldhelpgc {
		// 检查是否需要触发gc
		if t := (gcTrigger{kind: gcTriggerHeap}); t.test() {
			// 调用gcStart开始gc
			gcStart(gcBackgroundMode, t)
		}
	}

	return x
}

// 大对象分配
func largeAlloc(size uintptr, needzero bool, noscan bool) *mspan {
	// print("largeAlloc size=", size, "\n")
	if size+_PageSize < size {
		throw("out of memory")
	}
	// 得到所需页数
	npages := size >> _PageShift
	if size&_PageMask != 0 {
		npages++
	}

	// Deduct credit for this span allocation and sweep if
	// necessary. mHeap_Alloc will also sweep npages, so this only
	// pays the debt down to npage pages.
	deductSweepCredit(npages*_PageSize, npages)

	s := mheap_.alloc(npages, makeSpanClass(0, noscan), true, needzero)
	if s == nil {
		throw("out of memory")
	}
	s.limit = s.base() + size
	heapBitsForSpan(s.base()).initSpan(s)
	return s
}

// implementation of new builtin
// compiler (both frontend and SSA backend) knows the signature
// of this function
// new 内建函数的实现
func newobject(typ *_type) unsafe.Pointer {
	return mallocgc(typ.size, typ, true)
}

//go:linkname reflect_unsafe_New reflect.unsafe_New
func reflect_unsafe_New(typ *_type) unsafe.Pointer {
	return newobject(typ)
}

// newarray allocates an array of n elements of type typ.
func newarray(typ *_type, n int) unsafe.Pointer {
	if n == 1 {
		return mallocgc(typ.size, typ, true)
	}
	if n < 0 || uintptr(n) > maxSliceCap(typ.size) {
		panic(plainError("runtime: allocation size out of range"))
	}
	return mallocgc(typ.size*uintptr(n), typ, true)
}

//go:linkname reflect_unsafe_NewArray reflect.unsafe_NewArray
func reflect_unsafe_NewArray(typ *_type, n int) unsafe.Pointer {
	return newarray(typ, n)
}

func profilealloc(mp *m, x unsafe.Pointer, size uintptr) {
	mp.mcache.next_sample = nextSample()
	mProf_Malloc(x, size)
}

// nextSample returns the next sampling point for heap profiling. The goal is
// to sample allocations on average every MemProfileRate bytes, but with a
// completely random distribution over the allocation timeline; this
// corresponds to a Poisson process with parameter MemProfileRate. In Poisson
// processes, the distance between two samples follows the exponential
// distribution (exp(MemProfileRate)), so the best return value is a random
// number taken from an exponential distribution whose mean is MemProfileRate.
func nextSample() int32 {
	if GOOS == "plan9" {
		// Plan 9 doesn't support floating point in note handler.
		if g := getg(); g == g.m.gsignal {
			return nextSampleNoFP()
		}
	}

	return fastexprand(MemProfileRate)
}

// fastexprand returns a random number from an exponential distribution with
// the specified mean.
func fastexprand(mean int) int32 {
	// Avoid overflow. Maximum possible step is
	// -ln(1/(1<<randomBitCount)) * mean, approximately 20 * mean.
	switch {
	case mean > 0x7000000:
		mean = 0x7000000
	case mean == 0:
		return 0
	}

	// Take a random sample of the exponential distribution exp(-mean*x).
	// The probability distribution function is mean*exp(-mean*x), so the CDF is
	// p = 1 - exp(-mean*x), so
	// q = 1 - p == exp(-mean*x)
	// log_e(q) = -mean*x
	// -log_e(q)/mean = x
	// x = -log_e(q) * mean
	// x = log_2(q) * (-log_e(2)) * mean    ; Using log_2 for efficiency
	const randomBitCount = 26
	q := fastrand()%(1<<randomBitCount) + 1
	qlog := fastlog2(float64(q)) - randomBitCount
	if qlog > 0 {
		qlog = 0
	}
	const minusLog2 = -0.6931471805599453 // -ln(2)
	return int32(qlog*(minusLog2*float64(mean))) + 1
}

// nextSampleNoFP is similar to nextSample, but uses older,
// simpler code to avoid floating point.
func nextSampleNoFP() int32 {
	// Set first allocation sample size.
	rate := MemProfileRate
	if rate > 0x3fffffff { // make 2*rate not overflow
		rate = 0x3fffffff
	}
	if rate != 0 {
		return int32(fastrand() % uint32(2*rate))
	}
	return 0
}

type persistentAlloc struct {
	base *notInHeap
	off  uintptr
}

var globalAlloc struct {
	mutex
	persistentAlloc
}

// Wrapper around sysAlloc that can allocate small chunks.
// There is no associated free operation.
// Intended for things like function/type/debug-related persistent data.
// If align is 0, uses default align (currently 8).
// The returned memory will be zeroed.
//
// Consider marking persistentalloc'd types go:notinheap.
func persistentalloc(size, align uintptr, sysStat *uint64) unsafe.Pointer {
	var p *notInHeap
	systemstack(func() {
		p = persistentalloc1(size, align, sysStat)
	})
	return unsafe.Pointer(p)
}

// Must run on system stack because stack growth can (re)invoke it.
// See issue 9174.
//go:systemstack
func persistentalloc1(size, align uintptr, sysStat *uint64) *notInHeap {
	const (
		chunk    = 256 << 10
		maxBlock = 64 << 10 // VM reservation granularity is 64K on windows
	)

	if size == 0 {
		throw("persistentalloc: size == 0")
	}
	if align != 0 {
		if align&(align-1) != 0 {
			throw("persistentalloc: align is not a power of 2")
		}
		if align > _PageSize {
			throw("persistentalloc: align is too large")
		}
	} else {
		align = 8
	}

	if size >= maxBlock {
		return (*notInHeap)(sysAlloc(size, sysStat))
	}

	mp := acquirem()
	var persistent *persistentAlloc
	if mp != nil && mp.p != 0 {
		persistent = &mp.p.ptr().palloc
	} else {
		lock(&globalAlloc.mutex)
		persistent = &globalAlloc.persistentAlloc
	}
	persistent.off = round(persistent.off, align)
	if persistent.off+size > chunk || persistent.base == nil {
		persistent.base = (*notInHeap)(sysAlloc(chunk, &memstats.other_sys))
		if persistent.base == nil {
			if persistent == &globalAlloc.persistentAlloc {
				unlock(&globalAlloc.mutex)
			}
			throw("runtime: cannot allocate memory")
		}
		persistent.off = 0
	}
	p := persistent.base.add(persistent.off)
	persistent.off += size
	releasem(mp)
	if persistent == &globalAlloc.persistentAlloc {
		unlock(&globalAlloc.mutex)
	}

	if sysStat != &memstats.other_sys {
		mSysStatInc(sysStat, size)
		mSysStatDec(&memstats.other_sys, size)
	}
	return p
}

// notInHeap is off-heap memory allocated by a lower-level allocator
// like sysAlloc or persistentAlloc.
//
// In general, it's better to use real types marked as go:notinheap,
// but this serves as a generic type for situations where that isn't
// possible (like in the allocators).
//
// TODO: Use this as the return type of sysAlloc, persistentAlloc, etc?
//
//go:notinheap
type notInHeap struct{}

func (p *notInHeap) add(bytes uintptr) *notInHeap {
	return (*notInHeap)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + bytes))
}

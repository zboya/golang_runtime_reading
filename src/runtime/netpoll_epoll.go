// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build linux
// https://www.jianshu.com/p/ee381d365a29
// EPOLL类似于POLL，是Linux特有的一种IO多路复用的机制。它在2.5.44内核中引入。
// 对于大量的描述符处理，EPOLL更有优势，它提供了三个系统调用来创建管理epoll实例：
// epoll_create创建一个epoll实例，返回该实例的文件描述符；
// epoll_ctl注册感兴趣的特定文件描述符，注册的描述符集合称为epoll集合；
// epoll_wait监听IO事件；

package runtime

import "unsafe"

func epollcreate(size int32) int32
func epollcreate1(flags int32) int32

//go:noescape
func epollctl(epfd, op, fd int32, ev *epollevent) int32

//go:noescape
func epollwait(epfd int32, ev *epollevent, nev, timeout int32) int32
func closeonexec(fd int32)

var (
	epfd int32 = -1 // epoll descriptor
)

// 网络轮询器的初始化，创建epoll的实例和设置它的文件描述符 epfd
func netpollinit() {
	epfd = epollcreate1(_EPOLL_CLOEXEC)
	if epfd >= 0 {
		return
	}
	epfd = epollcreate(1024)
	if epfd >= 0 {
		closeonexec(epfd)
		return
	}
	println("runtime: epollcreate failed with", -epfd)
	throw("runtime: netpollinit failed")
}

func netpolldescriptor() uintptr {
	return uintptr(epfd)
}

// EPOLLIN - 当关联的文件可以执行 read ()操作时。
// EPOLLOUT - 当关联的文件可以执行 write ()操作时。
// EPOLLRDHUP - (从 linux 2.6.17 开始)当socket关闭的时候，或者半关闭写段的(当使用边缘触发的时候，这个标识在写一些测试代码去检测关闭的时候特别好用)
// EPOLLPRI - 当 read ()能够读取紧急数据的时候。
// EPOLLERR - 当关联的文件发生错误的时候，epoll_wait() 总是会等待这个事件，并不是需要必须设置的标识。
// EPOLLHUP - 当指定的文件描述符被挂起的时候。epoll_wait() 总是会等待这个事件，并不是需要必须设置的标识。当socket从某一个地方读取数据的时候(管道或者socket),这个事件只是标识出这个已经读取到最后了(EOF)。所有的有效数据已经被读取完毕了，之后任何的读取都会返回0(EOF)。
// EPOLLET - 设置指定的文件描述符模式为边缘触发，默认的模式是水平触发。
// EPOLLONESHOT - (从 linux 2.6.17 开始)设置指定文件描述符为单次模式。这意味着，在设置后只会有一次从epoll_wait() 中捕获到事件，之后你必须要重新调用 epoll_ctl() 重新设置。
// 作者：大呀大帝国
// 链接：https://www.jianshu.com/p/ee381d365a29
// 来源：简书
// 简书著作权归作者所有，任何形式的转载都请联系作者获得授权并注明出处。
func netpollopen(fd uintptr, pd *pollDesc) int32 {
	var ev epollevent
	ev.events = _EPOLLIN | _EPOLLOUT | _EPOLLRDHUP | _EPOLLET
	*(**pollDesc)(unsafe.Pointer(&ev.data)) = pd  // ev.data 指向 pd
	return -epollctl(epfd, _EPOLL_CTL_ADD, int32(fd), &ev)
}

func netpollclose(fd uintptr) int32 {
	var ev epollevent
	return -epollctl(epfd, _EPOLL_CTL_DEL, int32(fd), &ev)
}

func netpollarm(pd *pollDesc, mode int) {
	throw("runtime: unused")
}

// polls for ready network connections
// returns list of goroutines that become runnable
// 返回可变成可运行的G列表
// 意思就是当底层epoll有事件通知时，就表示该G是就绪的G
// 举例：
// 	一个goroutine因为net io读取阻塞了，此时goroutine会进入Gwaiting状态
// 	当有一个数据发送给这个goroutine io fd时，如果M执行了netpoll函数，就会获取到这个G
//  这就是golang实现底层用非阻塞io来实现用户层阻塞io的一部分。
func netpoll(block bool) *g {
	if epfd == -1 {
		return nil
	}
	waitms := int32(-1)
	// 如果是非阻塞的调用，设置waitms = 0
	if !block {
		waitms = 0
	}
	var events [128]epollevent
retry:
	// sys_linux_amd64.s
	n := epollwait(epfd, &events[0], int32(len(events)), waitms)
	if n < 0 {
		if n != -_EINTR {
			println("runtime: epollwait on fd", epfd, "failed with", -n)
			throw("runtime: netpoll failed")
		}
		goto retry
	}
	var gp guintptr
	for i := int32(0); i < n; i++ {
		ev := &events[i]
		if ev.events == 0 {
			continue
		}
		var mode int32
		if ev.events&(_EPOLLIN|_EPOLLRDHUP|_EPOLLHUP|_EPOLLERR) != 0 {
			mode += 'r'
		}
		if ev.events&(_EPOLLOUT|_EPOLLHUP|_EPOLLERR) != 0 {
			mode += 'w'
		}
		// mode不等于0，表示有epoll事件通知
		if mode != 0 {
			pd := *(**pollDesc)(unsafe.Pointer(&ev.data))

			netpollready(&gp, pd, mode)
		}
	}
	if block && gp == 0 {
		goto retry
	}
	return gp.ptr()
}

本文档原文来自runtime的[HACKING.md]()  
这是一份活文件，有时会过时。它旨在阐明Go运行时的编程与编写普通Go的不同之处。它侧重于普遍的概念而不是特定接口的细节。

Scheduler structures
====================

调度程序管理遍及运行时的三种类型的资源：Gs，Ms和Ps。即使您没有使用调度程序，理解这些也很重要。

Gs, Ms, Ps
----------

`G`只是一个goroutine。它由`g`类型表示。当goroutine退出时，它的g对象将返回到一个闲置`g`s池中，以后可以重新用于其他goroutine。

`M`是可以执行用户Go代码，运行时代码，系统调用或空闲的OS线程。它由`m`类型表示。由于系统调用中可能阻塞任意数量的线程，因此一次可以有任意数量的`M`s（当然前提是不能超过maxmcount数量）。

最后，`P`表示执行用户Go代码所需的资源，例如调度程序和内存分配器状态。它由`p`类型表示。确切地说是`GOMAXPROCS`个Ps。`P`可以大概被认为是OS调度程序中的CPU，而`p`类型的内容就像每个CPU状态一样。这是一个放置需要分片以提高效率的状态的好地方，但不需要是每线程或每个goroutine都需要P。

调度程序的工作是匹配`G`（要执行的代码），`M`（执行它的位置）和`P`（执行它的权限和资源）。当M停止执行用户Go代码时，例如通过输入系统调用，它将其`P`返回到空闲`P`池。为了恢复执行用户Go代码，例如在从系统调用返回时，它必须从空闲池获取`P`。

所有`g`，`m`和`p`对象都是堆分配的，但从不释放，因此它们的内存保持类型稳定。因此，运行时可以避免调度程序深度中的写入障碍。


User stacks and system stacks
-----------------------------

每个非死G都有一个与之关联的用户堆栈，这是用户Go代码执行的内容。用户堆栈从小开始（例如，2K）并动态增长或缩小。

每个M都有一个与之关联的系统堆栈（也称为M的“g0”堆栈，因为它实现为存根G），在Unix平台上，还有一个信号堆栈（也称为M的“gsignal”堆栈）。系统和信号堆不能增长，但足够大，可以执行运行时和cgo代码（纯Go二进制中的8K; cgo二进制中的系统分配）。

运行时代码通常使用systemstack，mcall或asmcgocall临时切换到系统堆栈，以执行必须不被抢占的任务，不得增加用户堆栈或切换用户goroutines。在系统堆栈上运行的代码隐式不可抢占，垃圾收集器不扫描系统堆栈。在系统堆栈上运行时，不会使用当前用户堆栈执行。


`getg()` and `getg().m.curg`
----------------------------
要获取当前用户g，请使用`getg().m.curg`。

`getg()`单独返回当前g，但是当在系统或信号堆栈上执行时，这将分别返回当前M的“g0”或“gsignal”。这通常不是你想要的。

要确定您是在用户堆栈还是系统堆栈上运行，请使用`getg() == getg().m.curg`。


Error handling and reporting
============================
可以从用户代码中合理恢复的错误应该像往常一样使用panic。但是，在某些情况下，panic会导致紧急致命错误，例如在系统堆栈上调用或在mallocgc期间调用时。

运行时中的大多数错误都无法恢复。对于这些，使用throw，它会转储回溯并立即终止进程。通常，throw应该传递一个字符串常量，以避免在危险的情况下分配。按照惯例，在使用print或println抛出之前会打印其他详细信息，并且消息的前缀为“runtime：”。

对于运行时错误调试，使用`GOTRACEBACK=system` 或 `GOTRACEBACK=crash`运行很有用。

Synchronization
===============

运行时具有多个同步机制。它们的语义不同，特别是它们是否与goroutine调度程序或OS调度程序交互。

最简单的是互斥锁，它使用锁定和解锁来操纵。这应该用于短期保护共享结构。阻塞互斥锁直接阻塞M，而不与Go调度程序交互。这意味着从运行时的最低级别使用是安全的，但也可以防止任何关联的G和P被重新安排。 rwmutex很相似。

对于一次性通知，请使用note，它提供notesleep和notewakeup。与传统的UNIX睡眠/唤醒不同，`note`是无竞争的，因此如果已经发生了notewakeup，则会立即返回。使用后可以使用noteclear重置音符，该音符不能与睡眠或唤醒竞赛。像互斥体一样，音符上的阻塞阻塞了M.但是，在音符上有不同的睡眠方式：notesleep还可以防止重新安排任何相关的G和P，而notetsleepg就像阻塞系统调用一样，允许P重复使用运行另一个G.这仍然比直接阻止G的效率低，因为它消耗了M.

要直接与goroutine调度程序交互，请使用gopark和goready。 gopark暂停当前的goroutine - 将其置于“等待”状态并将其从调度程序的运行队列中删除 - 并在当前的M / P上安排另一个goroutine。 goready将停放的goroutine置于“runnable”状态并将其添加到运行队列中。

综上所述，

<table>
<tr><th></th><th colspan="3">Blocks</th></tr>
<tr><th>Interface</th><th>G</th><th>M</th><th>P</th></tr>
<tr><td>(rw)mutex</td><td>Y</td><td>Y</td><td>Y</td></tr>
<tr><td>note</td><td>Y</td><td>Y</td><td>Y/N</td></tr>
<tr><td>park</td><td>Y</td><td>N</td><td>N</td></tr>
</table>

Unmanaged memory
================

一般来说，runtime尝试使用普通的堆分配的内存。 然而，有些时候runtime必须使用不在gc堆上的内存，(即 unmanaged memeory) 如果对象是内存管理系统本身的一部分或者在某些调用者不含P的情况下分配的。 (即这部分内存不受gc控制，是更底层的接口，直接用mmap分配的)

分配非托管内存有三种方式:

sysAlloc 直接从OS获取内存。可以获取任何系统页大小(4k)整数倍的内存， 也可以被sysFree释放

persistentalloc 吧多个小内存组合为单次sysAlloc防止内存碎片。 然而，没有办法释放其分配的内存。

fixalloc 是slab风格的分配器，用于分配固定大小的对象。 fixalloc分配的对象可以被释放，但是这个内存可能会被fixalloc pool复用， 因此它只能给相同类型的对象复用。

一般来说，任何通过上面这些分配的类型都应该标记为//go:notinheap

非托管内存分配的对象 禁止 含有堆指针，除非一下满足以下条件:

非托管内存的人和指针都必须明确地加入到gc的根对象(runtime.markroot)。

如果内存复用，堆指针必须在被gc根可见之前0初始化。 否则，gc会看到陈旧的堆指针。 参考 "Zero-initialization versus zeroing"


Zero-initialization versus zeroing
==================================

运行时有两种类型的归零，具体取决于内存是否已初始化为类型安全状态。

如果内存不是类型安全的状态，意味着它可能包含“垃圾”，因为它刚刚被分配并且它被初始化以供第一次使用，那么它必须使用memclrNoHeapPointers或非指针写入进行零初始化。这不会执行写入障碍。

如果内存已经处于类型安全状态并且只是设置为零值，则必须使用常规写入，typedmemclr或memclrHasPointers来完成。这会执行写入障碍。


Runtime-only compiler directives
================================
除了“go doc compile”中记录的“// go：”指令之外，编译器仅在运行时支持其他指令。

go:systemstack
--------------
go：systemstack表示函数必须在系统堆栈上运行。这是由特殊功能序言动态检查的。


go:nowritebarrier
-----------------

如果以下函数包含任何写入障碍，则nowritebarrier会指示编译器发出错误。 （它不会抑制写屏障的产生;它只是一个断言。）

通常你想要去：nowritebarrierrec。 go：nowritebarrier主要用于“不好”没有写入障碍但不是正确性所必需的情况。

go:nowritebarrierrec and go:yeswritebarrierrec
----------------------------------------------

go：nowritebarrierrec指示编译器发出错误，如果以下函数或它以递归方式调用的任何函数，直到go：yeswritebarrierrec，包含写屏障。

逻辑上，编译器从每个go：nowritebarrierrec函数开始泛洪调用图，如果遇到包含写屏障的函数则会产生错误。此洪水在go：yeswritebarrierrec函数停止。

go：nowritebarrierrec用于执行写屏障以防止无限循环。

这两个指令都在调度程序中使用。写入障碍需要一个活动的P（getg（）。mp！= nil）并且调度程序代码通常在没有活动P的情况下运行。在这种情况下，go：nowritebarrierrec用于释放P的函数或者可以在没有P的情况下运行并且去：当代码重新获取活动P时使用yeswritebarrierrec。由于这些是功能级注释，因此释放或获取P的代码可能需要分为两个函数。

go:notinheap
------------

go：notinheap适用于类型声明。它表示永远不能从GC'd堆分配类型。具体来说，指向此类型的指针必须始终无法通过runtime.inheap检查。
该类型可用于全局变量，堆栈变量或非托管内存中的对象（例如，使用sysAlloc，persistentalloc，fixalloc或手动管理的跨度分配）。特别：

new（T），make（[] T），append（[] T，...）和T的隐式堆分配是不允许的。 （尽管在运行时中不允许隐式分配。）

指向常规类型（unsafe.Pointer除外）的指针无法转换为指向go：notinheap类型的指针，即使它们具有相同的基础类型。

任何包含go：notinheap类型的类型本身都是：notinheap。结构和数组是：如果它们的元素是，那就不是了。 
go：notinheap类型的地图和频道是不允许的。为了保持显式，任何类型声明隐含的类型声明：notinheap必须明确标记go：notinheap。

在指针上写下障碍：notinheap类型可以省略。

最后一点是go的真正好处：notinheap。运行时将它用于低级内部结构，以避免调度程序和内存分配器中的内存障碍，
它们是非法的或效率低下的。这种机制相当安全，不会影响运行时的可读性。

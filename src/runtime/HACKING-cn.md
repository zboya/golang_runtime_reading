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

通常，运行时尝试使用常规堆分配。但是，在某些情况下，运行时必须在非托管内存中分配垃圾收集堆之外的对象。如果对象是内存管理器本身的一部分，或者必须在调用者可能没有P的情况下分配它们，则这是必要的。

分配非托管内存有三种机制：

sysAlloc直接从OS获取内存。这是系统页面大小的整数倍，但可以使用sysFree释放。

persistentalloc将多个较小的分配组合到一个sysAlloc中以避免碎片。但是，没有办法释放persistentalloced对象（因此名称）。

fixalloc是一个SLAB样式的分配器，用于分配固定大小的对象。 fixalloced对象可以被释放，但是这个内存只能由同一个fixalloc池重用，所以它只能被重用于同一类型的对象。

通常，使用其中任何一个分配的类型应标记为// go：notinheap（见下文）。

除非遵守以下规则，否则在非托管内存中分配的对象不得包含堆指针：

从非托管内存到堆的任何指针都必须作为显式垃圾收集根添加到runtime.markroot中。

如果重用内存，则堆指针在作为GC根目录可见之前必须进行零初始化。否则，GC可能会观察过时的堆指针。请参阅“零初始化与归零”。


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









# Go 运行时编程
本文可能会过时。 它旨在阐明不同于常规 Go 编程的 Go 运行时编程，并侧重于一般概念而非特定接口的详细信息。

调度器结构
调度器管理了三种不同类型的资源分布在运行时中：G, M, P。 即便你进行不涉及调度器的相关工作，理解它们仍然很重要。

Gs, Ms, Ps

“G” 是 goroutine 的缩写。由 g 类型表示。当 goroutine 退出时，g 被归还到有效的 g 池， 能够被后续 goroutine 使用。

“M” 代表一个能够执行用户 Go 代码、运行时代码、系统调用或处于空闲状态的 OS thread。 由类型 m 表示。因为任意多个线程可以同时阻塞在系统调用上，因此同一时刻可以包含任意多个 M。

“P” 则表示执行 Go 代码所需的资源，例如调度器和内存分配器状态。由 p 类型表示,且最多只有 GOMAXPROCS 个。 P 可以理解一个 OS 调度器中的 CPU，p 类型的内容类似于每个 CPU 的状态。是一个可以为了效率 而存放不同状态的地方，同时还不需要每个线程或者每个 goroutine 对应一个 P。

调度器的任务是匹配一个 G（要执行的代码）一个 M（在哪儿执行）和一个 P（执行代码的资源和权利）。 当 M 停止执行用户 Go 代码时，例如进入系统调用，它会将其对应的 P 返回给空闲 P 池。 而为了恢复执行用户的 Go 代码，例如从一个系统调用返回时，必须从空闲池中获取一个有效的 P。

所有的 g, m 和 p 对象均在堆上分配，且从不释放。因此他们的内存保持 type stable。 因此，运行时可以在调度器内避免 write barrier。

用户栈与系统栈

每个非 dead 状态的 G 均被关联到用户栈上，即用户 Go 代码执行的地方。用户栈初始大小很小（例如 2K），然后动态的增加或减少。

每个 M 均对应一个关联的系统栈（也成为 M 的 g0 栈，因为其作为一个 stub G 来实现）。在 Unix 平台上，则称为信号栈（也称之为 M 的 gsignal 栈）。系统和信号栈无法扩展，但已经大到足够运行运行时和 cgo 代码（一个纯 Go 二进制文件有 8K；而 cgo 二进制文件则有系统分配）。

运行时代码经常会临时通过 systemstack, mcall 或 asmcgocall 切换到系统栈以执行那些无法扩展用户栈、或切换用户 goroutine 的不可抢占式任务。运行在系统栈上的代码是隐式不可抢占的、且不会被垃圾回收检测。当运行在系统栈上时，当前用户栈没有被用于执行代码。

getg() 与 getg().m.curg

为获取当前用户 g，可以使用 getg().m.curg。

getg() 单独返回当前 g。但当执行在系统或信号栈时，会返回当前 M 的 g0 或 gsignal。这通常可能不是你想要的。

为判断你是运行在用户栈还是系统栈，可以使用：getg() == getg().m.curg

错误处理与报告
通常使用 panic 的来自用户代码的错误能够被合理的恢复。然而某些情况下 panic 会导致一个立即致命错误，例如在系统栈上的调用或在 mallocgc 执行阶段的调用。

大部分运行时错误无法恢复。对于这些错误，请使用 throw，它能够将被终止整个过程的中间状态全部回溯出来。一般情况下，throw 需要传递一个 string 常量在危险情况下避免内存分配。按照惯例，在 throw 前会使用 print 或 println 输出额外信息，并冠以 “runtime:” 的前缀。

若需调试运行时错误，可以设置 GOTRACEBACK=system 或 GOTRACEBACK=crash。

同步
运行时有多种同步机制。它们根据与 goroutine 调度器或者 OS 调度器的交互不同而有着不同的语义。

最简单的是 mutex，使用 lock 和 unlock 进行操作。用于在短时间内共享结构体。在 mutex 上阻塞会直接阻塞 M，且不会与 Go 调度器进行交互。这也就意味着它能在运行时最底层是安全的，且同时阻止了任何与之关联的 G 和 P 被重新调度。rwmutex 也类似。

为进行一次性通知，可以使用 note。它提供了 notesleep 和 notewakeup。与传统的 UNIX sleep/wakeup 不同，note 是无竞争的。因此notesleep 会在 notewakeup 发生时立即返回。一个 note 能够在使用 noteclear 后被重置，并且必须不能与 sleep 或 wakeup 产生竞争。类似于 mutex，阻塞在 note 上会阻塞 M。然而有很多不同的方式可以在 note 上进行休眠：notesleep 会阻止关联的 G 和 P 被重新调度，而 notetsleepg 的行为类似于阻止系统调用、运行 P 被复用从而运行另一个 G。这种方式仍然比直接 阻塞一个 G 效率低，因为它还消耗了一个 M。

为直接与 goroutine 调度器进行交互，可以使用 gopark、goready。gopark 停摆了一个当前的 goroutine，并将其放入 waiting 状态，并将其从调度器运行队列中移除，再进一步将其他 goroutine 在当前 M/P 上进行调度。而 goready 会将一个停摆的 goroutine 标回 runable 状态，并加入运行队列中。

总结：

阻塞
接口	G	M	P
(rw)mutex	是	是	是
note	是	是	是/否
park	是	否	否
原子操作
运行时拥有自己的原子操作包，位于 runtime/internal/atomic。其对应于 sync/atomic，但处于历史原因函数具有不同的名字以及一些额外的运行时所需的函数。

一般来说，我们都仔细考虑了在运行始终使用这些原子操作，并尽可能避免了不必要的原子操作。如果某个时候访问一个某些时候会被其他同步机制保护的变量，那么访问已经受保护的内容通常不需要成为原子操作，原因如下：

在适当的时候使用非原子或原子操作访问会使代码更明确。对于变量的原子访问意味着其他地方可能同时访问该变量。
非原子访问能够自动进行竞争检查。运行时目前并没有竞争检查器，但未来可能会有。运行竞争检查器来检查你非原子访问的假设时，原子访问会破坏竞争检查器。
非原子访问能够提升性能。
当然，对一个共享变量进行任何非原子访问需要解释为什么该访问是受到保护的。

混合原子访问和费原子访问的常见模式为：

读通过写锁进行的变量，在锁区域内，读不需要原子操作，而写是需要的。在锁定区外，读是需要原子操作的。

读取仅发生在 STW 期间（Stop-The-World）。在 STW 期间不会发生写入 STW，即不需要原子操作。

换句话说，Go 内存模型的建议是：『不要太聪明』。运行时的性能很重要，但它的健壮性更重要。

非托管内存
通常，运行时尝试使用常规堆分配。然而，在某些情况下，运行时必须非托管内存中分配垃圾回收堆之外的对象。如果对象是内存管理器的一部分，或者必须在调用者可能没有 P 的情况下分他们，则这些分配和回收是有必要的。

分配非托管内存有三种机制：

sysAlloc 直接从 OS 获取内存。这会是系统页大小的整数倍，但也可以使用 sysFree 进行释放。

persistentalloc 将多个较小的分配组合到一个 sysAlloc 中避免碎片。但没有办法释放 persistentalloc 对象（所以叫这个名字）。

fixalloc 是一个 SLAB 样式的分配器，用于分配固定大小的对象。fixalloced 对象可以被释放，但是这个内存只能由同一个 fixalloc 池重用，所以它只能被重用于同一类型的对象。

一般来说，使用其中任何一个分配的类型应标记为 //go:notinheap （见下文）。

在非托管内存中分配对象不得包含堆指针，除非遵循下列原则：

任何来自非托管内存的堆指针必须在 runtime.markroot 中添加为显式垃圾回收的 root。
If the memory is reused, the heap pointers must be zero-initialized before they become visible as GC roots. Otherwise, the GC may observe stale heap pointers. See “Zero-initialization versus zeroing”. 如果内存被重用，那么堆指针必须进行在他们作为 GC root 可见前进行零初始化。否则，GC 可能会回收已经过时的堆指针。请参考「零初始化与归零」
零初始化 v.s. 归零
运行时有两种类型的归零方式，具体取决于内存是否已经初始化为类型安全状态。

如果内存不是类型安全的状态，那么它可能包含「垃圾」，因为它刚刚被分配且被初始化以供第一次使用，因此它必须使用 memclrNoHeapPointers 或非指针写进行零初始化。这不会执行 write barrier。

如果内存已经处于类型安全状态，并且只设置为零值，则必须使用常规写，通过 typedmemclr 或 memclrHasPointers 完成。这会执行 write barrier。

运行时独占的编译标志
除了 “go doc compile” 文档中说明的 “//go:” 标志外，编译器还未运行时支持了额外的标志。

go:systemstack

go:systemstack 表示函数必须在系统堆栈上运行。由特殊的函数序言（function prologue，指汇编程序函数开头的几行代码，通常是寄存器准备）进行动态检查。

go:nowritebarrier

如果函数包含 write barrier，则 go:nowritebarrier 触发一个编译器错误（它不会抑制 write barrier 的产生，只是一个断言）。

你通常希望 go:nowritebarrierrec。go:nowritebarrier 主要适用于没有 write barrier 会更好的情况，但没有要求正确性。

go:nowritebarrierrec 和 go:yeswritebarrierrec

.如果声明的函数或任何它递归调用的函数甚至于 go:yeswritebarrierrec 包含 write barrier，则 go:nowritebarrierrec 触发编译器错误。

逻辑上，编译器为每个函数调用补充 go:nowritebarrierrec 且当遭遇包含 write barrier 函数的时候产生一个错误。这种补充在 go:yeswritebarrierrec 函数上停止。

go:nowritebarrierrec 用于防止 write barrier 实现中的无限循环。

两个标志都在调度器中使用。write barrier 需要一个活跃的 P （getg().m.p != nil）且调度器代码通常在没有活跃 P 的情况下运行。在这种情况下，go:nowritebarrierrec 用于释放 P 的函数上，或者可以在没有 P 的情况下运行。而且go:nowritebarrierrec 还被用于当代码重新要求一个活跃的 P 时。由于这些都是函数级标注，因此释放或获取 P 的代码可能需要分为两个函数。

这两个指令都在调度程序中使用。 write barrier 需要一个活跃的P（ getg().mp != nil）并且调度程序代码通常在没有活动 P 的情 况下运行。在这种情况下，go：nowritebarrierrec用于释放P的函数或者可以在没有P的情况下运行并且去 ：当代码重新获取活动P时使用yeswritebarrierrec。由于这些是功能级注释，因此释放或获取P的代码可能需要分为两个函数。

go:notinheap

go:notinheap 适用于类型声明。它表明一种不能从 GC 堆中分配的类型。具体来说，指向此类型必须让 runtime.inheap 检查失败。类型可能是用于全局变量，堆栈变量或用于对象非托管内存（例如使用 sysAlloc 分配、persistentalloc、fixalloc 或手动管理的范围）。特别的：

new(T), make([]T), append([]T, ...) 以及 T 的隐式堆分配是不允许的（尽管运行时中无论如何都是不允许隐式分配的）。

指向常规类型（ unsafe.Pointer 除外）的指针不能转换为指向 go:notinheap 类型，即使他们有相同的基础类型。

任何包含 go:notinheap 类型的类型本身也是 go:notinheap 的。结构和数组中如果元素是 go:notinheap 的则自生也是。go:notinheap 类型的 map 和 channel 是不允许的。为使所有事情都变得显式，任何隐式 go:notinheap 类型的声明必须显式的声明 go:notinheap。

指向 go:notinheap 类型的指针上的 write barrier 可以省略。

最后一点是 go:notinheap 真正的好处。运行时会使用它作为低级别内部结构使用来在内存分配器和调度器中避免 非法或简单低效的 memory barrier。这种机制相当安全且没有牺牲运行时代码的可读性。
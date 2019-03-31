# 初定方案
**有任何理解错误的地方，还望指出**

## golang官网
* [golang.org](https://golang.org)
* [github.com/golang/go](https://github.com/golang/go)

## 目标
理解golang runtime的运行原理，重点掌握golang的调度，gc，内存分配，数据结构. 

`对于注释不理解的，欢迎提issue`。

## 目前的进度
* 2018-08-05 已阅读完调度系统的大概源码
* 2018-08-12 正在仔细阅读调度的源码
* 2018-08-19 正在仔细阅读调度的源码
* 2018-08-27 正在仔细阅读调度的源码
* 2018-09-02 已仔细阅读完调度的源码，正在阅读gc的大概源码
* 2018-09-09 正在阅读gc的大概源码
* 2018-09-16 大概阅读完gc流程源码
* 2018-09-24 开始详细阅读gc源码
* 2018-10-13 理解mgc的注释和大概阅读gcStart
* 2018-10-20 阅读gcMark准备和markroots扫描根对象的逻辑
* 2018-10-27 阅读gc的栈扫描和消费标记队列
* 2018-11-04 内存分配的注释 (@jingyugao)
* 2018-11-25 简单看了一下系统调用如何调度 
* 2018-01-12 开始阅读内存分配
* 2019-01-19 补充gc的整个流程和继续阅读内存分配
* 2019-02-10 继续阅读内存分配
* 2019-02-22 基本阅读完内存分配的流程，接下来阅读栈的分配
* 2019-03-03 阅读栈管理的代码
* 2019-03-18 基本阅读完stack的分配
* 2019-03-30 阅读golang网络底层原理和Mutex的实现


## 微信群
想一起阅读的小伙伴可以加我微信`sheepbao-520`,加入阅读群
![wechat](./wechat.jpeg)  

## github地址
https://github.com/sheepbao/golang_runtime_reading

### 时间
每周日晚9:00-10:00

### golang版本
go1.10.2

### 准备工作
* 有一台能上网的电脑
* 安装zoom软件，并注册
* 装一个阅读golang源码的编译器或者ide，推荐vscode
* 下载go1.10.2的源码

### 可以先阅读的资料
* [Goroutine背后的系统知识](http://blog.jobbole.com/35304/)
* [golang源码剖析-雨痕老师](https://github.com/qyuhen/book)
* [go-intervals](https://github.com/teh-cmc/go-internals)
* [也谈goroutine调度器](https://tonybai.com/2017/06/23/an-intro-about-goroutine-scheduler/)

### 活动步骤
* 线上用zoom共享屏幕，阅读golang runtime源码，一起讨论添加注释，尽量让每个人都理解
* 提交结果到github

### 阅读的方式
1. 选好一个主题，并查询资料阅读该主题的相关背景
2. 大概浏览阅读相关源码
3. 仔细阅读源码实现原理
4. 最后再整理整个流程

### 暂定的主题
1. goroutine调度实现
2. 数据结构的实现
3. 内存分配实现
4. gc的实现


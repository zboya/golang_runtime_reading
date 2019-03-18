// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sys

// Declarations for runtime services implemented in C or assembly.

// PtrSize表示指针的size，也代表了cpu架构是32位还是64位。
const PtrSize = 4 << (^uintptr(0) >> 63) // unsafe.Sizeof(uintptr(0)) but an ideal const
// Uintreg的size，不通平台不通大小，32bit为4，64bit为8
const RegSize = 4 << (^Uintreg(0) >> 63)           // unsafe.Sizeof(uintreg(0)) but an ideal const
const SpAlign = 1*(1-GoarchArm64) + 16*GoarchArm64 // SP alignment: 1 normally, 16 for ARM64

var DefaultGoroot string // set at link time

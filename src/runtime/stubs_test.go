package runtime_test

import (
	"log"
	"runtime"
	"testing"
)

func testFunc() {
	pc := runtime.Getcallerpc()
	// sp := getcallersp(unsafe.Pointer(&arg1))
	log.Printf("pc: 0x%x", pc)
}

func TestGetcallerpc(t *testing.T) {
	// log.Printf("pc1: 0x%x", runtime.Getcallerpc())
	testFunc()
	log.Printf("func addr: %p", testFunc)
	n := 9
	_ = n
	log.Printf("n addr: %p", &n)
}

/*
2018/08/10 18:34:42 pc: 0x1217306
2018/08/10 18:34:42 func addr: 0x1217230
2018/08/10 18:34:42 n addr: 0xc420096628
*/

package main

/*
#include <dlfcn.h>
#include <stdlib.h>
static const char* self_path(void) {
	Dl_info info;
	if (dladdr((void*)self_path, &info) && info.dli_fname) {
		return info.dli_fname;
	}
	return "";
}
#cgo LDFLAGS: -ldl
*/
import "C"

func dylibPath() string { return C.GoString(C.self_path()) }

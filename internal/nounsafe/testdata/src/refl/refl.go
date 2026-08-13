package refl

import "reflect"

func Bad(v reflect.Value) any {
	return v.UnsafePointer() // want `reflect.UnsafePointer is not verified. Check its safety and the adapter's safety yourself`
}

func Headers() {
	var _ reflect.SliceHeader  // want `reflect.SliceHeader is not verified. Check its safety and the adapter's safety yourself`
	var _ reflect.StringHeader // want `reflect.StringHeader is not verified. Check its safety and the adapter's safety yourself`
}

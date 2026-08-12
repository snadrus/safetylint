package refl

import "reflect"

func Bad(v reflect.Value) any {
	return v.UnsafePointer() // want `reflect.UnsafePointer launders pointers and escapes Go memory safety`
}

func Headers() {
	var _ reflect.SliceHeader  // want `reflect.SliceHeader launders pointers and escapes Go memory safety`
	var _ reflect.StringHeader // want `reflect.StringHeader launders pointers and escapes Go memory safety`
}

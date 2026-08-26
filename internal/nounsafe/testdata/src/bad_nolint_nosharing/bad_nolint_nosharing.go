package bad_nolint_nosharing

//nolint:nosharing
import _ "unsafe" // want `import "unsafe" is not verified. Check its safety and the adapter's safety yourself`

package a

import _ "unsafe" // want `import "unsafe" escapes Go memory safety`

func F() {}

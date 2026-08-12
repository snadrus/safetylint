package nosharing_test

import (
	"testing"

	"safetylint/internal/nosharing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestNosharing(t *testing.T) {
	testdata := analysistest.TestData()
	analysistest.Run(t, testdata, nosharing.Analyzer,
		"bad_capture",
		"bad_mutex",
		"bad_ptrarg",
		"bad_global",
		"bad_freeze_send",
		"bad_freeze_recv",
		"bad_mutex_unlockedread",
		"bad_mutex_partial",
		"bad_rwmutex",
		"bad_globalfreeze",
		"bad_twomutex",
		"bad_trylock",
		"good_channels",
		"good_freeze",
		"good_readonly",
		"good_nested_client",
		"bad_nested_write",
		"good_waitgroup",
		"good_valuechan",
		"good_mutexstruct",
		"good_mutex_parent",
		"good_trylock",
		"good_globalinit",
		"good_globalmain",
		"good_globalmutex",
		"good_global_read",
		"bad_global_httpserve",
		"sharero",
		"sharemu",
		"sharewrap",
		"bad_xpkg_readonly",
		"bad_xpkg_mutex",
		"bad_xpkg_wrap",
		"good_xpkg_mutex",
		"bad_afterfunc",
		"bad_handlefunc",
		"bad_initgo",
		"good_initgo",
		"plainlib",
		"hotlib",
		"bad_init_xpkg",
		"bad_init_xpkg_nomu",
		"bad_init_xpkg_readhot",
		"good_init_xpkg",
		"good_init_xpkg_read",
	)
}

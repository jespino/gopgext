package pg

import (
	"unsafe"

	"gopgext/internal/core"
)

// Settings are custom configuration parameters (GUCs) named
// "<extension>.<name>", that can be changed with SET, ALTER SYSTEM,
// postgresql.conf, ... They must be defined while the library loads: in
// main or a package-level variable initializer.
//
//	var greeting = pg.NewStringSetting("greeting", "Hello", "How hello() greets.")
//	...
//	greeting.Get()

// SettingOption modifies a setting.
type SettingOption int

// Superuser makes a setting changeable by superusers only (PGC_SUSET).
const Superuser SettingOption = 1

//export gopgext_guc_define
func gucDefine(kind int64, name, desc *byte, ival int64, fval float64, sval *byte,
	imin, imax int64, fmin, fmax float64, superuser bool, storage *unsafe.Pointer) uintptr

//export gopgext_loading
func loading() bool

func define(kind int64, name, desc string, ival int64, fval float64, sval *byte,
	imin, imax int64, fmin, fmax float64, opts []SettingOption) unsafe.Pointer {
	if !loading() {
		panic("pg: settings must be defined while the library loads (in main or a package initializer)")
	}
	superuser := false
	for _, o := range opts {
		superuser = superuser || o == Superuser
	}
	var p unsafe.Pointer
	full := ExtensionName() + "." + name
	core.Check(gucDefine(kind, core.CBytes(full), core.CBytes(desc), ival, fval, sval,
		imin, imax, fmin, fmax, superuser, &p))
	return p
}

// BoolSetting is a boolean setting.
type BoolSetting struct{ p *bool }

// NewBoolSetting defines a boolean setting.
func NewBoolSetting(name string, def bool, desc string, opts ...SettingOption) *BoolSetting {
	var i int64
	if def {
		i = 1
	}
	return &BoolSetting{(*bool)(define(0, name, desc, i, 0, nil, 0, 0, 0, 0, opts))}
}

// Get returns the current value.
func (s *BoolSetting) Get() bool { return *s.p }

// IntSetting is an integer setting (32 bits).
type IntSetting struct{ p *int32 }

// NewIntSetting defines an integer setting with a default and a range.
func NewIntSetting(name string, def, min, max int, desc string, opts ...SettingOption) *IntSetting {
	return &IntSetting{(*int32)(define(1, name, desc, int64(def), 0, nil, int64(min), int64(max), 0, 0, opts))}
}

// Get returns the current value.
func (s *IntSetting) Get() int { return int(*s.p) }

// FloatSetting is a floating-point setting.
type FloatSetting struct{ p *float64 }

// NewFloatSetting defines a floating-point setting with a default and a range.
func NewFloatSetting(name string, def, min, max float64, desc string, opts ...SettingOption) *FloatSetting {
	return &FloatSetting{(*float64)(define(2, name, desc, 0, def, nil, 0, 0, min, max, opts))}
}

// Get returns the current value.
func (s *FloatSetting) Get() float64 { return *s.p }

// StringSetting is a string setting.
type StringSetting struct{ p **byte }

// NewStringSetting defines a string setting.
func NewStringSetting(name, def, desc string, opts ...SettingOption) *StringSetting {
	return &StringSetting{(**byte)(define(3, name, desc, 0, 0, core.CBytes(def), 0, 0, 0, 0, opts))}
}

// Get returns the current value.
func (s *StringSetting) Get() string { return core.GoString(*s.p) }

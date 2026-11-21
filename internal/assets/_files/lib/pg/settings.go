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

// SettingOption modifies a setting: who can change it, and when.
type SettingOption int

const (
	// Superuser makes a setting changeable by superusers only (PGC_SUSET).
	Superuser SettingOption = 1
	// Sighup makes a setting changeable only in postgresql.conf (or ALTER
	// SYSTEM) and on the command line, taking effect on a configuration
	// reload (PGC_SIGHUP).
	Sighup SettingOption = 2
	// Postmaster makes a setting changeable only at server start
	// (PGC_POSTMASTER): for libraries in shared_preload_libraries, e.g. a
	// setting that sizes shared memory.
	Postmaster SettingOption = 3

	// Units of an integer or floating-point setting: its value can be given
	// with a unit ("10s", "1MB") and SHOW prints one.
	UnitBytes        SettingOption = 1 << 8
	UnitKilobytes    SettingOption = 2 << 8
	UnitMilliseconds SettingOption = 3 << 8
	UnitSeconds      SettingOption = 4 << 8
	UnitMinutes      SettingOption = 5 << 8
)

//export gopgext_guc_define
func gucDefine(kind int64, name, desc *byte, ival int64, fval float64, sval *byte,
	imin, imax int64, fmin, fmax float64, context int64, options **byte, noptions int64,
	storage *unsafe.Pointer) uintptr

//export gopgext_loading
func loading() bool

func define(kind int64, name, desc string, ival int64, fval float64, sval *byte,
	imin, imax int64, fmin, fmax float64, options []string, opts []SettingOption) unsafe.Pointer {
	if !loading() {
		panic("pg: settings must be defined while the library loads (in main or a package initializer)")
	}
	context := int64(0) // the most restrictive option wins
	var unit int64
	for _, o := range opts {
		if o >= UnitBytes {
			unit = int64(o)
		} else {
			context = max(context, int64(o))
		}
	}
	context |= unit
	var copts []*byte
	for _, o := range options {
		copts = append(copts, core.CBytes(o))
	}
	var popts **byte
	if len(copts) > 0 {
		popts = &copts[0]
	}
	var p unsafe.Pointer
	full := ExtensionName() + "." + name
	core.Check(gucDefine(kind, core.CBytes(full), core.CBytes(desc), ival, fval, sval,
		imin, imax, fmin, fmax, context, popts, int64(len(copts)), &p))
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
	return &BoolSetting{(*bool)(define(0, name, desc, i, 0, nil, 0, 0, 0, 0, nil, opts))}
}

// Get returns the current value.
func (s *BoolSetting) Get() bool { return *s.p }

// IntSetting is an integer setting (32 bits).
type IntSetting struct{ p *int32 }

// NewIntSetting defines an integer setting with a default and a range.
func NewIntSetting(name string, def, min, max int, desc string, opts ...SettingOption) *IntSetting {
	return &IntSetting{(*int32)(define(1, name, desc, int64(def), 0, nil, int64(min), int64(max), 0, 0, nil, opts))}
}

// Get returns the current value.
func (s *IntSetting) Get() int { return int(*s.p) }

// FloatSetting is a floating-point setting.
type FloatSetting struct{ p *float64 }

// NewFloatSetting defines a floating-point setting with a default and a range.
func NewFloatSetting(name string, def, min, max float64, desc string, opts ...SettingOption) *FloatSetting {
	return &FloatSetting{(*float64)(define(2, name, desc, 0, def, nil, 0, 0, min, max, nil, opts))}
}

// Get returns the current value.
func (s *FloatSetting) Get() float64 { return *s.p }

// StringSetting is a string setting.
type StringSetting struct{ p **byte }

// NewStringSetting defines a string setting.
func NewStringSetting(name, def, desc string, opts ...SettingOption) *StringSetting {
	return &StringSetting{(**byte)(define(3, name, desc, 0, 0, core.CBytes(def), 0, 0, 0, 0, nil, opts))}
}

// Get returns the current value.
func (s *StringSetting) Get() string { return core.GoString(*s.p) }

// EnumSetting is a setting whose value is one of a list of names.
type EnumSetting struct {
	p      *int32
	values []string
}

// NewEnumSetting defines a setting that takes one of values (the default
// must be one of them; names are matched case-insensitively).
func NewEnumSetting(name, def string, values []string, desc string, opts ...SettingOption) *EnumSetting {
	i := -1
	for j, v := range values {
		if v == def {
			i = j
		}
	}
	if i < 0 {
		panic("pg: the default of enum setting " + name + " must be one of its values")
	}
	return &EnumSetting{(*int32)(define(4, name, desc, int64(i), 0, nil, 0, 0, 0, 0, values, opts)), values}
}

// Get returns the current value (one of the values).
func (s *EnumSetting) Get() string { return s.values[*s.p] }

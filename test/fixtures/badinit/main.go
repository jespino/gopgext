// badinit's main panics: loading the library fails, and keeps failing.
package main

//gopgext:function
func Answer() int32 { return 42 }

func main() { panic("bad configuration") }

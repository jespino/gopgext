// oom exhausts its (small) Go heap: the backend ends with FATAL, the server
// and other sessions are unaffected.
package main

var keep [][]byte

//gopgext:function
func Hog(mib int32) int64 {
	for i := 0; i < int(mib); i++ {
		keep = append(keep, make([]byte, 1<<20))
	}
	return int64(len(keep))
}

func main() {}

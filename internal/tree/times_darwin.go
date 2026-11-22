package tree

import "syscall"

func times(st *syscall.Stat_t) (modTime, changeTime int64) {
	return st.Mtimespec.Nano(), st.Ctimespec.Nano()
}

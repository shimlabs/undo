package tree

import "syscall"

func statTimes(st *syscall.Stat_t) (modTime, changeTime int64) {
	return st.Mtim.Nano(), st.Ctim.Nano()
}

package tree

import "syscall"

func statTimes(stat *syscall.Stat_t) (modTime, changeTime int64) {
	return stat.Mtim.Nano(), stat.Ctim.Nano()
}

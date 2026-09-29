//go:build !darwin && !linux

package main

import "errors"

func restartUpdatedExecutable(string) error {
	return errors.New("当前平台请下载发行包后手动重启")
}

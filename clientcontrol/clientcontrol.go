package clientcontrol

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ClientControl struct {
	rmMutex    sync.RWMutex
	md5        string
	apprunpath string
	data       []byte
}

func New(apprunpath string) (*ClientControl, error) {
	fb, err := os.ReadFile(filepath.Join(apprunpath, "temp", "modulebalancingclient.exe"))
	if err != nil {
		return nil, err
	}

	return &ClientControl{
		rmMutex:    sync.RWMutex{},
		md5:        fmt.Sprintf("%x", md5.Sum(fb)),
		apprunpath: apprunpath,
		data:       fb,
	}, nil
}

func (the *ClientControl) Monitor() {
	ticker := time.NewTicker(time.Minute)
	for {
		select {
		case <-ticker.C:
			the.rmMutex.Lock()
			fb, err := os.ReadFile(filepath.Join(the.apprunpath, "temp", "modulebalancingclient.exe"))
			if err != nil {
				the.rmMutex.Unlock()
				continue
			}

			if fmt.Sprintf("%x", md5.Sum(fb)) == the.md5 {
				the.rmMutex.Unlock()
				continue
			}

			the.md5 = fmt.Sprintf("%x", md5.Sum(fb))
			the.data = fb
			the.rmMutex.Unlock()
		}
	}
}

func (the *ClientControl) MD5() string {
	the.rmMutex.RLock()
	defer the.rmMutex.RUnlock()
	return the.md5
}

func (the *ClientControl) Data() []byte {
	the.rmMutex.RLock()
	defer the.rmMutex.RUnlock()
	return the.data
}

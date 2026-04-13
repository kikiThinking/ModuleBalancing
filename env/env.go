/*
*

	@author: kiki
	@since: 2025/5/28
	@desc: //TODO

*
*/

package env

import (
	"ModuleBalancing/db"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/crc64"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"context"

	"github.com/fsnotify/fsnotify"
	"github.com/redmask-hb/GoSimplePrint/goPrint"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"
)

const (
	monitorFileReadyInterval = 5 * time.Second
	monitorFileReadyAttempts = 120
)

type Configuration struct {
	Setting struct {
		Expiration            int64  `yaml:"Expiration"`
		CheckExpiration       int64  `yaml:"CheckExpiration"`
		CheckUnwanted         int    `yaml:"CheckUnwanted"`
		CheckClientExpiration int64  `yaml:"CheckClientExpiration"`
		Common                string `yaml:"Common"`
	} `yaml:"Setting"`
	Database struct {
		Host     string `yaml:"Host"`
		Port     string `yaml:"Port"`
		Username string `yaml:"Username"`
		Password string `yaml:"Password"`
	} `yaml:"Database"`
	GRPC struct {
		Port string `yaml:"Port"`
	} `yaml:"GRPC"`
	Backup []struct {
		Host string `yaml:"Host"`
		Port string `yaml:"Port"`
	} `yaml:"Backup"`
}

type Accumulate struct {
	Server string `yaml:"Server"`
	Size   int64  `yaml:"Size"`
}

type Processprintstruct struct {
	Size        int
	conversion  int
	processspri *goPrint.Bar
}

var (
	err error
)

func CRC64(filePath string, chunkSize int64, workers int) (uint64, int64, error) {
	f, err := os.OpenFile(filePath, os.O_RDONLY, 0644)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open file: %v", err)
	}
	defer f.Close()

	finformation, err := f.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("filed to get file information: %v", err)
	}

	fileSize := finformation.Size()
	if fileSize == 0 {
		return 0, finformation.Size(), err
	}

	chunks := int((fileSize + chunkSize - 1) / chunkSize)

	table := crc64.MakeTable(crc64.ECMA)
	var wg sync.WaitGroup
	results := make([]uint64, chunks) // 使用切片存储结果，保持顺序
	workCh := make(chan int, chunks)
	errCh := make(chan error, 1)
	var hasError bool
	var mu sync.Mutex // 用于保护hasError

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for chunkIndex := range workCh {
				offset := int64(chunkIndex) * chunkSize
				size := chunkSize
				if offset+size > fileSize {
					size = fileSize - offset
				}

				buf := make([]byte, size)
				n, err := f.ReadAt(buf, offset)
				if err != nil && err != io.EOF {
					mu.Lock()
					if !hasError {
						errCh <- fmt.Errorf("读取文件块 %d 失败: %v", chunkIndex, err)
						hasError = true
					}
					mu.Unlock()
					return
				}

				sum := crc64.Checksum(buf[:n], table)
				results[chunkIndex] = sum
			}
		}()
	}

	go func() {
		for i := 0; i < chunks; i++ {
			workCh <- i
		}
		close(workCh)
	}()

	go func() {
		wg.Wait()
		close(errCh) // 关闭错误通道表示所有工作完成
	}()

	if err := <-errCh; err != nil {
		return 0, finformation.Size(), err
	}

	finalCRC := uint64(0)
	for i := 0; i < chunks; i++ {
		sum := results[i]
		finalCRC = crc64.Update(finalCRC, table, []byte{
			byte(sum >> 56), byte(sum >> 48), byte(sum >> 40), byte(sum >> 32),
			byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
		})
	}

	return finalCRC, finformation.Size(), nil
}

func Analyzing(ctx *gorm.DB, cf Configuration, source []byte, logmar *logmanager.BusinessLogger) ([]string, []string, error) {
	var (
		crifilelist    = make([]string, 0)
		response       = make([]string, 0)
		analyzingerror = make([]string, 0)
		buf            = bufio.NewScanner(bytes.NewReader(source))
	)

	matchModule, err := regexp.Compile(`(?i)FILE=.*CRI`)
	if err != nil {
		return nil, nil, err
	}

	// 按行读取AOD文件, 匹配Cri
	for buf.Scan() {
		if buf.Err() != nil {
			break
		}

		var criFileName = strings.NewReplacer("FILE=", "", "File=", "").Replace(matchModule.FindString(buf.Text()))
		if strings.EqualFold(criFileName, "") {
			continue
		}

		crifilelist = append(crifilelist, strings.TrimSpace(criFileName))
	}

	matchModule, err = regexp.Compile(`(?i)modulename\d*.*`) //
	if err != nil {
		return nil, nil, err
	}

	var Analyzingmodules = func(source []byte) []string {
		var res = make([]string, 0)
		buf = bufio.NewScanner(bytes.NewBuffer(source))

		for buf.Scan() {
			if buf.Err() != nil {
				break
			}

			var modulename = matchModule.FindString(buf.Text())
			if strings.EqualFold(modulename, "") || len(strings.Split(modulename, "=")) != 2 {
				continue
			}

			res = append(res, strings.TrimSpace(strings.Split(modulename, "=")[1]))
		}
		return res
	}

	// 读取本地Cri文件 并匹配其中的Module列表
	for _, value := range crifilelist {
		var exist bool
		if err = ctx.Model(db.Module{}).Select(`COUNT(*) > 0`).Where(db.Module{Name: value}).Scan(&exist).Error; err != nil {
			return nil, nil, err
		}

		// 关键代码, 防止文件不存在记录存在的情况, 导致读取文件错误
		_, fexist := os.Stat(strings.Join([]string{cf.Setting.Common, value}, `\`))
		if !exist || os.IsNotExist(fexist) {
			// from backup
			for _, backupserver := range cf.Backup {
				backctx, cencel := context.WithCancel(context.Background())
				if err = Downloadmodulefromback(ctx, backctx, fmt.Sprintf("%s:%s", backupserver.Host, backupserver.Port), cf.Setting.Common, value, cf.Setting.Expiration, logmar); err != nil {
					logmar.Error(fmt.Sprintf("Failed to from backup download module: %s", err.Error()))
					cencel()
					analyzingerror = append(analyzingerror, value)
					continue
				}

				cencel()
				break
			}
		}

		response = append(response, strings.TrimSpace(value))
		fbyte, err := os.ReadFile(strings.Join([]string{cf.Setting.Common, value}, `\`))
		if err != nil {
			return nil, nil, err
		}

		response = append(response, Analyzingmodules(fbyte)...)
	}

	return response, analyzingerror, nil
}

func waitForFileReady(fp string) (bool, error) {
	for i := 0; i < monitorFileReadyAttempts; i++ {
		if _, err := os.Stat(fp); err != nil {
			if os.IsNotExist(err) {
				time.Sleep(monitorFileReadyInterval)
				continue
			}
			return false, err
		}

		handle, err := windows.CreateFile(
			windows.StringToUTF16Ptr(fp),
			windows.GENERIC_READ,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL,
			0,
		)
		if err == nil {
			_ = windows.CloseHandle(handle)
			return true, nil
		}

		time.Sleep(monitorFileReadyInterval)
	}

	return false, nil
}

func upsertMonitoredModule(ctx *gorm.DB, module db.Module) error {
	var isexistrecord bool
	if err = ctx.Unscoped().Model(db.Module{}).Select(`COUNT(*) > 0`).Where(db.Module{Name: module.Name}).Scan(&isexistrecord).Error; err != nil {
		return err
	}

	if isexistrecord {
		return ctx.Unscoped().Model(db.Module{}).Where(db.Module{Name: module.Name}).
			Updates(map[string]interface{}{
				"crc64":      module.CRC64,
				"size":       module.Size,
				"lastuse":    module.Lastuse,
				"expiration": module.Expiration,
				"deleted_at": nil,
			}).Error
	}

	return ctx.Model(db.Module{}).Create(&module).Error
}

func processMonitoredFile(ctx *gorm.DB, logwri *logmanager.BusinessLogger, expiration int64, fp string) {
	ready, waitErr := waitForFileReady(fp)
	if waitErr != nil {
		fmt.Printf("%s ----> Failed\n", filepath.Base(fp))
		logwri.Error(waitErr.Error())
		return
	}

	if !ready {
		fmt.Printf("%s ----> Failed\n", filepath.Base(fp))
		logwri.Error(fmt.Sprintf("The file has been occupied for more than 10 minutes(%s)", fp))
		return
	}

	crc, size, crcErr := CRC64(fp, 128*1024*1024, 0)
	if crcErr != nil {
		fmt.Printf("%s ----> Failed\n", filepath.Base(fp))
		logwri.Error(crcErr.Error())
		return
	}

	module := db.Module{
		CRC64:      crc,
		Name:       filepath.Base(fp),
		Size:       size,
		Lastuse:    time.Now(),
		Expiration: time.Now().Add(time.Hour * 24 * time.Duration(expiration)),
	}

	if err := upsertMonitoredModule(ctx, module); err != nil {
		fmt.Printf("%s ----> Failed\n", module.Name)
		logwri.Error(err.Error())
		return
	}

	fmt.Printf("%s ----> OK\n", module.Name)

	logwri.Info(fmt.Sprintf("Create a new module record ----> %-20s  Size: %-10v  CRC64: %-20v  Lastuse:%-20s  Expiration:%-20s",
		module.Name,
		module.Size,
		module.CRC64,
		module.Lastuse.Format(`2006-01-02 15:04:05`),
		module.Expiration.Format(`2006-01-02 15:04:05`),
	))
}

func MonitorModule(ctx *gorm.DB, logwri *logmanager.BusinessLogger, expiration int64, monitorpath string) {
	logwri.Info(fmt.Sprintf("starting monitor ----> (%s)", monitorpath))
	var (
		monitorfile = make(chan string, 200)
		pending     = make(map[string]struct{})
		pendingMu   sync.Mutex
	)

	go func() {
		for fp := range monitorfile {
			processMonitoredFile(ctx, logwri, expiration, fp)
			pendingMu.Lock()
			delete(pending, fp)
			pendingMu.Unlock()
		}
	}()

	monitordir, err := fsnotify.NewWatcher()
	if err != nil {
		logwri.Error(fmt.Sprintf("Monitor Path NewWatcher Error: %s", err.Error()))
		return
	}

	if err = monitordir.Add(monitorpath); err != nil {
		logwri.Error(fmt.Sprintf("Add Monitor Path Error: %s", err.Error()))
		return
	}
	defer monitordir.Close()

	var number = 1
	for {
		select {
		case cre := <-monitordir.Events:
			if cre.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
				inf, err := os.Stat(cre.Name)
				if err != nil {
					logwri.Error(err.Error())
					continue
				}

				if inf.IsDir() {
					continue
				}

				if strings.Contains(cre.Name, "frombackdownload") {
					logwri.Info(fmt.Sprintf("file(%s) from backup server download, skip check!", cre.Name))
					continue
				}

				pendingMu.Lock()
				if _, ok := pending[cre.Name]; ok {
					pendingMu.Unlock()
					continue
				}
				pending[cre.Name] = struct{}{}
				pendingMu.Unlock()

				monitorfile <- cre.Name
				number++
			}
		case err := <-monitordir.Errors:
			logwri.Error(fmt.Sprintf("Panic: Monitor Path: %s", err.Error()))
			continue
		}
	}
}

// Downloadmodulefromback from backup server download module
func Downloadmodulefromback(dbcontrol *gorm.DB, ctx context.Context, backupaddress, fp, fn string, expirationday int64, logmar *logmanager.BusinessLogger) error {
	var (
		stream grpc.ServerStreamingClient[rpc.ModulePushResponse]
		conn   *grpc.ClientConn
		reload = false
	)

	// dstfp 目标文件名
	// tempfp 防止monitor函数自动去解析中backup server download的文件
	var (
		dstfp  = fmt.Sprintf("%s/%s", fp, fn)
		tempfp = fmt.Sprintf("%s/%s-frombackdownload", fp, fn)
	)

	//return errors.New("Not change local store")

	logmar.Info(fmt.Sprintf("from backup server download module  ----> %s", filepath.Base(fn)))
	conn, err = grpc.NewClient(backupaddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logmar.Error(fmt.Sprintf("Did not connect: %s", err.Error()))
		return err
	}

	defer conn.Close()

	dlclient := rpc.NewModuleClient(conn)

	// 判断文件夹是否存在, 如果不存在创建文件夹, 防止创建文件时发生panic
	if _, exist := os.Stat(fp); os.IsNotExist(exist) {
		if err = os.MkdirAll(fp, 0777); err != nil {
			return err
		}
	}

RELOAD:
	if stream, err = dlclient.Push(ctx, &rpc.ModuleDownloadRequest{Filename: fn, Offset: 0, Serveraddress: "1111"}); err != nil {
		return err
	}

	f, err := os.Create(tempfp)
	if err != nil {
		return err
	}

	headers, err := stream.Header()
	if err != nil {
		return err
	}

	if len(headers) == 0 {
		return errors.New("failed to did not receive the headers passed by the server")
	}

	var (
		crc   = headers.Get("crc64")
		size  int64
		Cunix int64
		Munix int64
		fcrc  uint64
	)

	filesize, err := strconv.ParseInt(headers.Get("size")[0], 10, 64)
	if err != nil {
		return err
	}

	var offset = 0

	var bar = Processprintstruct{Size: int(filesize)}
	bar.Initialization()

	for {
		var response *rpc.ModulePushResponse
		if response, err = stream.Recv(); err != nil {
			if err == io.EOF {
				// 下载完成
				break
			}

			// 这里实现断点续传, 如果重新和服务端建立连接则重offset处继续下载
			fmt.Println("Service connect close, wait downloading...")
			var connect = false
			for i := 0; i <= 12; i++ {
				log.Printf("waiting of retry offset(%v)...\t\n", offset)
				time.Sleep(time.Second * 5)
				if stream, err = dlclient.Push(ctx, &rpc.ModuleDownloadRequest{Filename: fn, Offset: int64(offset)}); err != nil {
					continue
				}
				connect = true
				break
			}

			if connect {
				continue
			}

			return fmt.Errorf("failed to download file(%s), retry more then 5 times", fn)
		}

		// 文件已经下载完毕
		if response.Completed {
			break
		}

		number, err := f.Write(response.Content)
		if err != nil {
			return err
		}

		offset += number
		bar.ProcessPrint(offset)
	}

	fmt.Printf("\r\nDownload (%s)\t----> finish\r\n\r\n", strings.Join([]string{fp, fn}, `\`))
	logmar.Info(fmt.Sprintf("Download (%s)\t----> finish", strings.Join([]string{fp, fn}, `\`)))

	_ = f.Close()

	if Munix, err = strconv.ParseInt(headers.Get("munix")[0], 10, 64); err != nil {
		return err
	}

	if Cunix, err = strconv.ParseInt(headers.Get("cunix")[0], 10, 64); err != nil {
		return err
	}

	if err = Changefiletime(tempfp, Cunix, Munix); err != nil {
		return err
	}

	// 计算下载下来的文件的CRC, 比对是否与服务端提供的一致
	if fcrc, size, err = CRC64(tempfp, 128*1024*1024, 0); err != nil {
		return err
	}

	// 判断下载的文件大小与服务端提供的是否一致
	if size != filesize || !strings.EqualFold(crc[0], strconv.FormatUint(fcrc, 10)) {
		if !reload {
			logmar.Info("File verification failed, request server to reload")
			reload = true
			ctxforreload, clsforreload := context.WithCancel(context.Background())
			defer clsforreload()
			if err = Modulereload(ctxforreload, conn, "1111", filepath.Base(fn)); err != nil {
				return fmt.Errorf("failed to reload module: %s", err.Error())
			}

			goto RELOAD
		}

		return fmt.Errorf("Backup(%s) Download(%s) the crc64 values are inconsistent, and the file may have been damaged during the download process ", crc[0], strconv.FormatUint(fcrc, 10))
	}

	var module = db.Module{
		CRC64:      fcrc,
		Name:       filepath.Base(fn),
		Size:       filesize,
		Lastuse:    time.Now(),
		Expiration: time.Now().Add(time.Hour * 24 * time.Duration(expirationday)),
	}

	var isexistrecord bool
	if err = dbcontrol.Unscoped().
		Model(db.Module{}).
		Select(`COUNT(*) > 0`).
		Where(db.Module{Name: module.Name}).
		Scan(&isexistrecord).Error; err != nil {
		return fmt.Errorf("failed to check exist (%s): %s", module.Name, err.Error())
	}

	if isexistrecord {
		if err = dbcontrol.
			Unscoped().
			Model(db.Module{}).
			Where(db.Module{Name: module.Name}).
			Updates(map[string]interface{}{
				"crc64":      module.CRC64,
				"size":       module.Size,
				"lastuse":    module.Lastuse,
				"expiration": module.Expiration,
				"deleted_at": nil,
			}).Error; err != nil {
			return fmt.Errorf("failed to unscope module %s: %w", module.Name, err)
		}
	} else {
		if err = dbcontrol.Model(db.Module{}).Create(&module).Error; err != nil {
			return fmt.Errorf("failed to create module %s: %w", module.Name, err)
		}
	}

	if err = os.Rename(tempfp, dstfp); err != nil {
		return fmt.Errorf("failed to rename file(%s --> %s)", tempfp, dstfp)
	}

	logmar.Info(fmt.Sprintf("Module download completed  --> %-20s  CRC: %-20v  Size: %-10v  Lastuse: %-20s  Expiration: %-20s",
		module.Name, module.CRC64, module.Size, module.Lastuse, module.Expiration,
	))

	return nil
}

// Uploadtoback backup module to back server
func Uploadtoback(ctx context.Context, backupaddress, common string, module db.Module, logmar *logmanager.BusinessLogger) error {
	var (
		conn *grpc.ClientConn
	)

	logmar.Info(fmt.Sprintf("Upload module(%s) to backup server", filepath.Base(module.Name)))
	conn, err = grpc.NewClient(backupaddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logmar.Error(fmt.Sprintf("Did not connect: %s", err.Error()))
		return err
	}

	defer conn.Close()

	uploadclient := rpc.NewModuleClient(conn)

	// 打开文件
	f, err := os.Open(strings.Join([]string{common, module.Name}, "/"))
	if err != nil {
		log.Fatalf("failed to open file: %v", err)
	}

	defer f.Close()

	finformation, err := f.Stat()
	if err != nil {
		return err
	}

	var fcreatedate int64
	switch runtime.GOOS {
	case "windows":
		fcreatedate = finformation.Sys().(*syscall.Win32FileAttributeData).CreationTime.Nanoseconds() / 1e9
	default:
		fcreatedate = time.Now().Unix()
	}

	stream, err := uploadclient.Upload(ctx)
	if err != nil {
		return err
	}

	if err := stream.Send(&rpc.UploadRequest{
		Data: &rpc.UploadRequest_Information{
			Information: &rpc.Finformation{
				Filename: module.Name,
				Size:     strconv.FormatInt(module.Size, 10),
				Crc64:    strconv.FormatUint(module.CRC64, 10),
				MUnix:    strconv.FormatInt(finformation.ModTime().Unix(), 10),
				CUnix:    strconv.FormatInt(fcreatedate, 10),
			}}}); err != nil {
		return err
	}

	// 分块上传文件数据
	buffer := make([]byte, 1*1024*1024)
	for {
		bytesize, err := f.Read(buffer)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("failed to read file: %v", err)
		}

		if err = stream.Send(&rpc.UploadRequest{
			Data: &rpc.UploadRequest_ChunkData{
				ChunkData: buffer[:bytesize],
			}}); err != nil {
			return err
		}
	}

	// 关闭并接收响应
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return err
	}

	if resp.Success {
		return nil
	}

	return errors.New(resp.Message)
}

func Changefiletime(fp string, munix, cunix int64) error {
	var (
		handle   syscall.Handle
		uint16fp *uint16
	)
	// 转换文件路径为UTF-16指针
	if uint16fp, err = syscall.UTF16PtrFromString(fp); err != nil {
		return err
	}

	// 打开文件获取句柄
	if handle, err = syscall.CreateFile(
		uint16fp,
		syscall.FILE_WRITE_ATTRIBUTES,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	); err != nil {
		return err
	}

	ParseWindowsTime := func(t time.Time) syscall.Filetime {
		return syscall.NsecToFiletime(t.UnixNano())
	}
	Ctime := ParseWindowsTime(time.Unix(cunix, 0))
	Mtime := ParseWindowsTime(time.Unix(munix, 0))
	Rtime := ParseWindowsTime(time.Now())
	defer syscall.CloseHandle(handle)
	return syscall.SetFileTime(handle, &Ctime, &Rtime, &Mtime)
}

// Modulereload Request the server to reload the module file
func Modulereload(ctx context.Context, conn *grpc.ClientConn, serverip, filename string) error {
	if _, err = rpc.NewModuleClient(conn).ModuleReload(ctx, &rpc.ModuleReloadRequest{Filename: filename, Serverip: serverip}); err != nil {
		return err
	}
	return nil
}

// AllowStorage Ask if the backup server can receive modules
func AllowStorage(ctx context.Context, backupaddress string, size int64, logmar *logmanager.BusinessLogger) bool {
	var (
		conn *grpc.ClientConn
	)

	conn, err = grpc.NewClient(backupaddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logmar.Error(fmt.Sprintf("[allow storage]Did not connect: %s", err.Error()))
		return false
	}

	defer conn.Close()

	askClient := rpc.NewModuleClient(conn)

	var allow *rpc.AllowStorageResponse
	allow, err = askClient.AllowStorage(ctx, &rpc.AllowStorageRequest{Size: size})
	if err != nil {
		logmar.Error("allow storage failed: " + err.Error())
		return false
	}

	return allow.Allow
}

// GetOptimalBufferSize 动态调整缓冲区大小
func GetOptimalBufferSize(fileSize int64) int {
	switch {
	case fileSize <= 0:
		return 4 * 1024 // 默认 4KB，处理异常情况

	case fileSize < 64*1024: // 小于 64KB
		return int(fileSize) // 小文件一次性读取

	case fileSize < 512*1024: // 小于 512KB
		return 64 * 1024 // 64KB 块

	case fileSize < 5*1024*1024: // 小于 5MB
		return 128 * 1024 // 128KB 块

	case fileSize < 20*1024*1024: // 小于 20MB
		return 512 * 1024 // 512KB 块

	case fileSize < 100*1024*1024: // 小于 100MB
		return 1 * 1024 * 1024 // 1MB 块

	default: // 100MB 及以上
		return 2 * 1024 * 1024 // 2MB 块
	}
}

func (the *Processprintstruct) Initialization() {
	const (
		KB = 1 << 10 // 1024
		MB = 1 << 20 // 1048576
		GB = 1 << 30 // 1073741824
		TB = 1 << 40 // 1099511627776
	)
	// 使用无表达式的switch进行范围判断
	switch {
	case the.Size < KB:
		the.conversion = 1
		the.processspri = goPrint.NewBar(the.Size)
		the.processspri.SetNotice("(Bytes)")
	case the.Size < MB:
		the.conversion = KB
		the.processspri = goPrint.NewBar(the.Size / KB)
		the.processspri.SetNotice("(KB)")
	case the.Size < GB:
		the.conversion = MB
		the.processspri = goPrint.NewBar(the.Size / MB)
		the.processspri.SetNotice("(MB)")

	default:
		the.conversion = TB
		the.processspri = goPrint.NewBar(the.Size / GB)
		the.processspri.SetNotice("(TB)")
	}
	the.processspri.SetGraph(`=`)
	the.processspri.SetEnds("|", "|")
}

func (the *Processprintstruct) ProcessPrint(size int) {
	the.processspri.PrintBar(size / the.conversion)
}

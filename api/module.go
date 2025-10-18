/*
*

	@author: kiki
	@since: 2025/5/26
	@desc: //TODO

*
*/

package api

import (
	"ModuleBalancing/db"
	"ModuleBalancing/env"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

var err error

type ModuleBalancing struct {
	Configuration *env.Configuration
	Dbcontrol     *gorm.DB
	Logmar        *logmanager.LogManager
	rpc.UnimplementedModuleServer
}

// IntegrityVerification 如果文件存在与本地 将会发起校验
func (the *ModuleBalancing) IntegrityVerification(_ context.Context, request *rpc.IntegrityVerificationRequest) (*rpc.IntegrityVerificationResponse, error) {
	var (
		model = new(db.Module)
	)

	if err = the.Dbcontrol.Where(db.Module{Name: request.Filename}).First(model).Error; err != nil {
		the.Logmar.GetLogger("IntegrityVerification").Error(err.Error())
		return nil, err
	}

	the.Logmar.GetLogger("IntegrityVerification").Info(fmt.Sprintf("Get module verification: %-20s  %-20v  %-20v", model.Name, model.Size, model.CRC64))

	return &rpc.IntegrityVerificationResponse{
		Filename: model.Name,
		Size:     strconv.FormatInt(model.Size, 10),
		Crc64:    strconv.FormatUint(model.CRC64, 10),
	}, nil
}

// Push 接收客户端的下载请求
func (the *ModuleBalancing) Push(request *rpc.ModuleDownloadRequest, stream rpc.Module_PushServer) error {
	the.Logmar.GetLogger("Download").Info(fmt.Sprintf("Client(%s) requests to download module(%s)", request.Serveraddress, request.Filename))
	var fp = strings.Join([]string{the.Configuration.Setting.Common, request.Filename}, `\`)

	var exist bool
	if err = the.Dbcontrol.Model(db.Module{}).Select(`COUNT(*) > 0`).Where(db.Module{Name: request.Filename}).Scan(&exist).Error; err != nil {
		the.Logmar.GetLogger("Push").Error(err.Error())
		return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
	}

	if exist {
		// 数据库中存在, 但是本地文件不存在, 尝试去Backup server下载
		if _, err = os.Stat(fp); os.IsNotExist(err) {

			the.Logmar.GetLogger("Download").Info("The file does not exist")

			ctx, downloadcancel := context.WithCancel(context.Background())
			defer downloadcancel()

			if err = env.Downloadmodulefromback(the.Dbcontrol, ctx, fmt.Sprintf("%s:%s", the.Configuration.Backup.Host, the.Configuration.Backup.Port), filepath.Dir(fp), request.Filename, the.Configuration.Setting.Expiration, the.Logmar.GetLogger("Download")); err != nil { // Backup server下载
				the.Logmar.GetLogger("Download").Error(fmt.Sprintf("Download failed %s", err.Error()))
				return err
			}
		}

	} else {
		if _, err = os.Stat(fp); os.IsNotExist(err) {

			the.Logmar.GetLogger("Download").Info("Database records and files do not exist")

			ctx, downloadcancel := context.WithCancel(context.Background())
			defer downloadcancel()

			if err = env.Downloadmodulefromback(the.Dbcontrol, ctx, fmt.Sprintf("%s:%s", the.Configuration.Backup.Host, the.Configuration.Backup.Port), filepath.Dir(fp), request.Filename, the.Configuration.Setting.Expiration, the.Logmar.GetLogger("Download")); err != nil { // Backup server下载
				the.Logmar.GetLogger("Download").Error(fmt.Sprintf("Download failed %s", err.Error()))
				return err
			}

		} else {
			the.Logmar.GetLogger("Download").Info("The database record does not exist but the file exists")
			var (
				crc  uint64
				size int64
			)

			if crc, size, err = env.CRC64(fp, 128*1024*1024, 8); err != nil {
				return err
			}

			if err = the.Dbcontrol.Model(db.Module{}).Create(&db.Module{
				CRC64:      crc,
				Name:       filepath.Base(fp),
				Size:       size,
				Lastuse:    time.Now(),
				Expiration: time.Now().Add(time.Hour * 24 * time.Duration(the.Configuration.Setting.Expiration)),
			}).Error; err != nil {
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
			}
		}
	}

	// 只要触发从backup server下载和本地解析都会触发
	var module = new(db.Module)
	if err = the.Dbcontrol.Model(db.Module{}).Where(db.Module{Name: request.Filename}).First(module).Error; err != nil {
		return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
	}

	the.Logmar.GetLogger("Download").Info(fmt.Sprintf("Hit %-20s  CRC: %-20v  Size: %-20v  Lastuse: %-20s  Expiration: %-20s",
		module.Name,
		module.CRC64,
		module.Size,
		module.Lastuse.Format(`2006-01-02 15:04:05`),
		module.Expiration.Format(`2006-01-02 15:04:05`)))

	the.Logmar.GetLogger("Download").Info(fmt.Sprintf("start download ----> filename(%s) seek(%v)", filepath.Base(fp), request.Offset))
	var offset = request.Offset
	f, err := os.OpenFile(fp, os.O_RDONLY, 0644)
	if err != nil {
		the.Logmar.GetLogger("Download").Error(fmt.Sprintf("failed to open file: %s", err.Error()))
		return err
	}

	defer f.Close()

	finformation, err := f.Stat()
	if err != nil {
		the.Logmar.GetLogger("Download").Error(fmt.Sprintf("failed to read file stat: %s", err.Error()))
		return err
	}

	var fcreatedate int64
	switch runtime.GOOS {
	case "windows":
		fcreatedate = finformation.Sys().(*syscall.Win32FileAttributeData).CreationTime.Nanoseconds() / 1e9
	default:
		fcreatedate = time.Now().Unix()
	}

	if err = stream.SendHeader(metadata.New(map[string]string{
		"size":        strconv.FormatInt(finformation.Size(), 10),
		"filename":    module.Name,
		"crc64":       strconv.FormatUint(module.CRC64, 10),
		"modify-time": strconv.FormatInt(finformation.ModTime().Unix(), 10),
		"create-time": strconv.FormatInt(fcreatedate, 10),
	})); err != nil {
		the.Logmar.GetLogger("Download").Error(fmt.Sprintf("failed to send headers: %s", err.Error()))
		return err
	}

	if offset > 0 {
		_, err := f.Seek(offset, io.SeekStart)
		if err != nil {
			return status.Errorf(codes.Internal, "failed to seek file: %v", err)
		}
	}

	var buffer = make([]byte, 1*1024*1024) // 1MB

	for {
		number, err := f.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break // 文件读取完毕
			}
			return status.Errorf(codes.Internal, "failed to read file: %v", err)
		}

		if err = stream.Send(&rpc.ModulePushResponse{
			Content:   buffer[:number],
			Completed: false,
		}); err != nil {
			return status.Errorf(codes.Internal, "failed to send chunk: %v", err)
		}
	}

	if err = stream.Send(&rpc.ModulePushResponse{
		Content:   []byte{},
		Completed: true,
	}); err != nil {
		return status.Errorf(codes.Internal, "failed to send chunk: %v", err)
	}

	the.Logmar.GetLogger("Download").Info(fmt.Sprintf("filename(%s) download completed", filepath.Base(fp)))

	if request.Serveraddress != "Debug" {
		var size int64
		if err = the.Dbcontrol.Model(db.Client{}).Select("accumulate_download").Where(db.Client{Serveraddress: request.Serveraddress}).Find(&size).Error; err != nil {
			the.Logmar.GetLogger("Download").Error(err.Error())
			return nil
		}

		if err = the.Dbcontrol.Model(db.Client{}).Where(db.Client{Serveraddress: request.Serveraddress}).Update("accumulate_download", size+module.Size).Error; err != nil {
			the.Logmar.GetLogger("Download").Error(err.Error())
			return nil
		}
		the.Logmar.GetLogger("Download").Info(fmt.Sprintf("Client: %s add download size: %d", request.Serveraddress, module.Size))
	}

	return nil
}

// Analyzing 客户端传递AOD文件, 解析后返回[Module]string
func (the *ModuleBalancing) Analyzing(_ context.Context, request *rpc.AnalyzingRequest) (*rpc.AnalyzingResponse, error) {
	var response = &rpc.AnalyzingResponse{
		Modulename:     make([]string, 0),
		Analyzingerror: make([]string, 0),
	}

	the.Logmar.GetLogger(`Analyzing`).Info(fmt.Sprintf("start analyzing ----> filename(%s)", request.Filename))
	if response.Modulename, response.Analyzingerror, err = env.Analyzing(the.Dbcontrol, *the.Configuration, request.Fbytes, the.Logmar.GetLogger(`Analyzing`)); err != nil {
		return nil, err
	}

	the.Logmar.GetLogger(`Analyzing`).Info(fmt.Sprintf("Completed(%d)\tFailed(%d)", len(response.Modulename), len(response.Analyzingerror)))
	the.Logmar.GetLogger(`Analyzing`).Info("----------------Completed----------------")
	for _, value := range response.Modulename {
		the.Logmar.GetLogger(`Analyzing`).Info(value)
	}

	if len(response.Analyzingerror) != 0 {
		the.Logmar.GetLogger(`Analyzing`).Info("----------------Failed----------------")
		for _, value := range response.Analyzingerror {
			the.Logmar.GetLogger(`Analyzing`).Info(value)
		}
	}

	return response, nil
}

// BreakClient 函数关闭客户端的连接
func (the *Expirationpush) BreakClient(clientID string) {
	the.mu.Lock()
	defer the.mu.Unlock()
	if ch, ok := the.ClientList[clientID]; ok {
		close(ch)
		delete(the.ClientList, clientID)
		log.Printf("GRPC Client (%s) removed", clientID)
	}
}

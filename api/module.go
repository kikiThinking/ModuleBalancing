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
	"errors"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	return the.Dbcontrol.Transaction(func(tx *gorm.DB) error {
		var reload = false // 标记是否需要重新加载 module 变量
		var module = new(db.Module)
		var fp = strings.Join([]string{the.Configuration.Setting.Common, request.Filename}, `\`)
		if err = tx.Model(db.Module{}).Where(db.Module{Name: request.Filename}).First(&module).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				the.Logmar.GetLogger("Download").Error("Not recorded in the database")
				if _, err = os.Stat(fp); os.IsNotExist(err) {
					the.Logmar.GetLogger("Download").Error("The file does not exist locally")
					ctx, cencel := context.WithCancel(context.Background())
					defer cencel()
					if err = env.Downloadmodulefromback(the.Dbcontrol, ctx, fmt.Sprintf("%s:%s", the.Configuration.Backup.Host, the.Configuration.Backup.Port), fp, request.Filename, the.Logmar.GetLogger("Download")); err != nil { // Backup server下载
						return err
					}
					// 数据库中没有记录, 本地文件也不存在, 尝试去Backup server下载
				} else {
					var crc uint64
					var size int64
					if crc, size, err = env.CRC64(fp, 128*1024*1024, 8); err != nil {
						return err
					}

					if err = tx.Model(db.Module{}).Create(&db.Module{
						CRC64:      crc,
						Name:       filepath.Base(fp),
						Size:       size,
						Lastuse:    time.Now(),
						Expiration: time.Now().Add(time.Hour * 24 * time.Duration(the.Configuration.Setting.Expiration)),
					}).Error; err != nil {
						return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
					}
					// 数据库中不存在, 但是本地存在文件, 解析本地文件写入数据库
				}
			} else {
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
			}
			reload = true
		} else {
			the.Logmar.GetLogger("Download").Info(fmt.Sprintf("Hit %-20s  CRC: %-20v  Size: %-20v  Lastuse: %-20s  Expiration: %-20s",
				module.Name,
				module.CRC64,
				module.Size,
				module.Lastuse.Format(`2006-01-02 15:04:05`),
				module.Expiration.Format(`2006-01-02 15:04:05`)))

			// 数据库中存在, 但是本地文件不存在, 尝试去Backup server下载
			if _, err = os.Stat(fp); os.IsNotExist(err) {
				the.Logmar.GetLogger("Download").Error("The file does not exist locally")
				ctx, cencel := context.WithCancel(context.Background())
				defer cencel()
				if err = env.Downloadmodulefromback(the.Dbcontrol, ctx, fmt.Sprintf("%s:%s", the.Configuration.Backup.Host, the.Configuration.Backup.Port), fp, request.Filename, the.Logmar.GetLogger("Download")); err != nil { // Backup server下载
					return err
				}
				reload = true
			}
		}

		// 只要触发从backup server下载和本地解析都会触发
		if reload {
			if err = tx.Model(db.Module{}).Where(db.Module{Name: request.Filename}).First(&module).Error; err != nil {
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
			}
		}

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

		module.Lastuse = time.Now()
		module.Expiration = time.Now().Add(time.Duration(the.Configuration.Setting.Expiration) * time.Hour * 24)

		if err = tx.Model(db.Module{}).Where(db.Module{Name: request.Filename}).Updates(&module).Error; err != nil {
			the.Logmar.GetLogger("Download").Error(fmt.Sprintf("failed to update database error --> %s", err.Error()))
		}

		the.Logmar.GetLogger("Download").Info(fmt.Sprintf("filename(%s) download completed", filepath.Base(fp)))
		return nil
	})
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

	the.Logmar.GetLogger(`Analyzing`).Info("Completed")
	for _, value := range response.Modulename {
		the.Logmar.GetLogger(`Analyzing`).Info(value)
	}

	the.Logmar.GetLogger(`Analyzing`).Info("Failed")
	for _, value := range response.Analyzingerror {
		the.Logmar.GetLogger(`Analyzing`).Info(value)
	}

	//这里判断数据库里面 有没有记录这个AOD 如果没有就记录一下 如果有就更新一下过期时间
	if err = the.Dbcontrol.Model(db.AOD{}).FirstOrCreate(&db.AOD{
		Name:       request.Filename,
		Size:       int64(len(request.Fbytes)),
		Lastuse:    time.Now(),
		Expiration: time.Now().Add(time.Hour * 24 * time.Duration(the.Configuration.Setting.Expiration)),
		Content:    request.Fbytes,
	}).Error; err != nil {
		the.Logmar.GetLogger("Analyzing").Error(fmt.Sprintf("failed to create database error --> %s", err.Error()))
		return nil, err
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

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
	pb "ModuleBalancing/grpc"
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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var err error

type ModuleBalancing struct {
	pb.UnimplementedModuleServer
	Configuration *env.Configuration
	Dbcontrol     *gorm.DB
}

type Storerecord struct {
	Dbcontrol *gorm.DB
	pb.UnimplementedStorerecordServer
}

type Expirationpush struct {
	mu         sync.Mutex
	Dbcontrol  *gorm.DB
	ClientList map[string]chan *pb.ExpirationPushResponse
	pb.UnimplementedExpirationpushServer
}

// Download 接收客户端的下载请求
func (the *ModuleBalancing) Download(request *pb.ModuleDownloadRequest, stream pb.Module_DownloadServer) error {
	return the.Dbcontrol.Transaction(func(tx *gorm.DB) error {
		var module = new(db.Module)
		if err = tx.Model(db.Module{}).Where(db.Module{Name: request.Filename}).First(&module).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 数据库中不存在, 从本地查找实体文件和BackServer Copy的逻辑
			} else {
				return err
			}
		}

		if _, err = os.Stat(strings.Join([]string{the.Configuration.Setting.Common, module.Name}, `\`)); os.IsNotExist(err) {
			// 实体文件不存在, 从BackServer Copy的逻辑
		}

		var offset = request.Offset
		f, err := os.OpenFile(strings.Join([]string{the.Configuration.Setting.Common, module.Name}, `\`), os.O_RDONLY, 0644)
		if err != nil {
			return err
		}

		defer f.Close()

		finformation, err := f.Stat()
		if err != nil {
			return err
		}

		if err = stream.SendHeader(metadata.New(map[string]string{"size": strconv.FormatInt(finformation.Size(), 10)})); err != nil {
			return err
		}

		if offset > 0 {
			_, err := f.Seek(offset, io.SeekStart)
			if err != nil {
				return status.Errorf(codes.Internal, "failed to seek file: %v", err)
			}
			log.Printf("Resuming download from offset: %d", offset)
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

			res := &pb.ModuleDownloadResponse{
				Content:   buffer[:number],
				Completed: false,
			}

			if err = stream.Send(res); err != nil {
				return status.Errorf(codes.Internal, "failed to send chunk: %v", err)
			}
		}

		if err = stream.Send(&pb.ModuleDownloadResponse{
			Content:   []byte{},
			Completed: true,
		}); err != nil {
			return status.Errorf(codes.Internal, "failed to send chunk: %v", err)
		}

		var fcreatedate int64
		switch runtime.GOOS {
		case "windows":
			fcreatedate = finformation.Sys().(*syscall.Win32FileAttributeData).CreationTime.Nanoseconds() / 1e9
		default:
			fcreatedate = time.Now().Unix()
		}

		fmt.Println("send trailer")
		stream.SetTrailer(metadata.New(map[string]string{
			"filename":    module.Name,
			"crc64":       strconv.FormatUint(module.CRC64, 10),
			"mod_time":    strconv.FormatInt(finformation.ModTime().Unix(), 10),
			"create_time": strconv.FormatInt(fcreatedate, 10),
		}))

		module.Lastuse = time.Now()
		module.Expiration = time.Now().Add(time.Duration(the.Configuration.Setting.Expiration) * time.Hour * 24)
		if err = tx.Model(db.Module{}).Where(db.Module{Name: request.Filename}).Updates(&module).Error; err != nil {
			log.Printf("failed to Update module(%s) ----> Message(%s)", module.Name, err.Error())
		}

		return nil
	})
}

// Analyzing 客户端传递AOD文件, 解析后返回[Module]string
func (the *ModuleBalancing) Analyzing(_ context.Context, request *pb.AnalyzingRequest) (*pb.AnalyzingResponse, error) {
	var response = &pb.AnalyzingResponse{
		Modulename:     make([]string, 0),
		Analyzingerror: make([]string, 0),
	}

	if response.Modulename, response.Analyzingerror, err = env.Analyzing(the.Dbcontrol, the.Configuration.Setting.Common, request.Fbytes); err != nil {
		return nil, err
	}

	log.Printf("%#v\n", the.Dbcontrol)

	//这里判断数据库里面 有没有记录这个AOD 如果没有就记录一下 如果有就更新一下过期时间
	if err = the.Dbcontrol.Model(db.AOD{}).FirstOrCreate(&db.AOD{
		Name:       request.Filename,
		Size:       int64(len(request.Fbytes)),
		Lastuse:    time.Now(),
		Expiration: time.Now().Add(time.Hour * 24 * time.Duration(the.Configuration.Setting.Expiration)),
		Content:    request.Fbytes,
	}).Error; err != nil {
		return nil, err
	}

	return response, nil
}

// Expiration 检查客户端的本地配置文件, 客户端文件的过期事情并通知删除
func (the *Expirationpush) Expiration(request *pb.ExpirationPushRequest, stream pb.Expirationpush_ExpirationServer) error {
	var messagechannel chan *pb.ExpirationPushResponse

	// 首次连接
	if _, ok := the.ClientList[request.Serveraddress]; !ok {
		the.ClientList[request.Serveraddress] = make(chan *pb.ExpirationPushResponse, 10)
		messagechannel = the.ClientList[request.Serveraddress]
	} else {
		messagechannel = the.ClientList[request.Serveraddress]
	}

	// 判断客户端是否存在记录, 如果不存在则写入db
	var clientconfiguration = new(db.Client)
	if err = the.Dbcontrol.Where(db.Client{Serveraddress: request.Serveraddress}).First(clientconfiguration).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Printf("Create new client ----> %s\r\n", request.Serveraddress)
			{
				clientconfiguration.Serveraddress = request.Serveraddress
				clientconfiguration.Maxretentiondays = request.Maxretentiondays
				clientconfiguration.Checkdir = request.Checkdir
				clientconfiguration.Outdir = request.Outdir
				clientconfiguration.Backdir = request.Backdir
				clientconfiguration.Store = make([]db.Clientmodule, 0)
			}
			if err = the.Dbcontrol.Model(db.Client{}).Create(clientconfiguration).Error; err != nil {
				return err
			}
		} else {
			return err
		}
	}

	log.Printf("GRPC Client connect ----> %s\r\n", request.Serveraddress)

	// 如果客户端传递的配置与数据库中的不一致则更新数据库
	if clientconfiguration.Maxretentiondays != request.Maxretentiondays ||
		clientconfiguration.Checkdir != request.Checkdir ||
		clientconfiguration.Backdir != request.Backdir ||
		clientconfiguration.Outdir != request.Outdir {
		if err = the.Dbcontrol.Model(db.Client{}).Where(db.Client{Serveraddress: request.Serveraddress}).Updates(map[string]interface{}{
			"maxretentiondays": request.Maxretentiondays,
			"checkdir":         request.Checkdir,
			"backdir":          request.Backdir,
			"outdir":           request.Outdir,
		}).Error; err != nil {
			return err
		}
	}

	// 客户端心跳逻辑
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			select {
			case <-stream.Context().Done():
				log.Printf("Client %s disconnected", request.Serveraddress)
				return
			default:
				select {
				case messagechannel <- &pb.ExpirationPushResponse{Heartbeat: request.Serveraddress}:
				default:
					log.Printf("Client %s channel is full, dropping message", request.Serveraddress)
				}
			}
		}
	}()

	// 发生Delete信息
	for {
		select {
		case message := <-messagechannel:
			if err := stream.Send(message); err != nil {
				the.BreakClient(request.Serveraddress)
				log.Printf("Failed to send message to client %s: %v", request.Serveraddress, err)
				return err
			}

		case <-stream.Context().Done():
			the.BreakClient(request.Serveraddress)
			return nil
		}
	}
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

// Updatestorerecord 函数接收客户端即将使用的Module列表, 也就是AOD解析出来的结果, 服务端接收这些结果用来记录过期时间通知客户端删除
func (the *Storerecord) Updatestorerecord(stream pb.Storerecord_UpdatestorerecordServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				err := stream.SendAndClose(&pb.StorerecordResponse{})
				if err != nil {
					// 记录错误日志
					log.Println("Error sending and closing stream:", err)
				}
				return nil
			} else {
				// 记录错误日志
				log.Println("Error receiving stream:", err)
				return err
			}
		}

		if strings.EqualFold(req.Heartbeat, "") {
			// 开启事务
			tx := the.Dbcontrol.Begin()
			// 使用 defer 确保事务回滚或提交

			var client = new(db.Client)
			if err = the.Dbcontrol.Preload(`Store`).Where(db.Client{Serveraddress: req.Serveraddress}).First(client).Error; err != nil {
				tx.Rollback() // 查询失败，回滚事务
				if errors.Is(err, gorm.ErrRecordNotFound) {
					// 记录错误日志
					log.Println("Client not found:", err)
					return err
				} else {
					// 记录错误日志
					log.Println("Error retrieving client:", err)
					return err
				}
			}

			for _, value := range req.Modulenames {
				found := false // 标记是否找到匹配的模块
				for j := range client.Store {
					if strings.EqualFold(client.Store[j].Name, value) {
						found = true
						client.Store[j].Partnumber = req.Partnumber
						client.Store[j].Expiration = time.Now().Add(time.Hour * 24 * time.Duration(client.Maxretentiondays))

						if err := tx.Model(&client.Store[j]).UpdateColumns(map[string]interface{}{
							"partnumber": req.Partnumber,
							"expiration": client.Store[j].Expiration,
						}).Error; err != nil {
							tx.Rollback()
							// 记录错误日志
							log.Println("Error updating module:", err)
							return err // 事务回滚并返回错误
						}
						break // 找到匹配的模块后，跳出内层循环
					}
				}
				if !found {
					// 没有找到对应的模块，创建新的 Clientmodule 记录
					newModule := db.Clientmodule{
						StoreID:    client.ID,      // 关联到当前的 Client
						Name:       value,          // 使用 req.Modulenames 中的 value 作为 Name
						Partnumber: req.Partnumber, // 使用 req.Partnumber
						Expiration: time.Now().Add(time.Hour * 24 * time.Duration(client.Maxretentiondays)),
					}

					if err := tx.Create(&newModule).Error; err != nil {
						tx.Rollback()
						// 记录错误日志
						log.Println("Error creating new module:", err)
						return err // 事务回滚并返回错误
					}

					// 将新创建的 module 添加到 client.Store 中 (可选，但推荐)
					client.Store = append(client.Store, newModule)

					//记录日志，说明创建了新的module
					log.Printf("Created new module %s for client %s\n", value, req.Serveraddress)
				}
			}

			if err := tx.Commit().Error; err != nil {
				tx.Rollback()
				// 记录错误日志
				log.Println("Error committing transaction:", err)
				return err // 提交失败，回滚并返回错误
			}

			if r := recover(); r != nil {
				tx.Rollback()
				panic(r) // 重新抛出panic
			}
		}
	}
}

/*
*

	@author: kiki
	@since: 2025/6/19
	@desc: //TODO

*
*/

package api

import (
	"ModuleBalancing/db"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type Expirationpush struct {
	mu         sync.Mutex
	Dbcontrol  *gorm.DB
	ClientList map[string]chan *rpc.ExpirationPushResponse
	Logmar     *logmanager.LogManager
	rpc.UnimplementedExpirationpushServer
}

// Expiration 检查客户端的本地配置文件, 客户端文件的过期事情并通知删除
func (the *Expirationpush) Expiration(request *rpc.ExpirationPushRequest, stream rpc.Expirationpush_ExpirationServer) error {
	var messagechannel chan *rpc.ExpirationPushResponse

	// 首次连接
	if _, ok := the.ClientList[request.Serveraddress]; !ok {
		the.ClientList[request.Serveraddress] = make(chan *rpc.ExpirationPushResponse, 10)
		messagechannel = the.ClientList[request.Serveraddress]
	} else {
		messagechannel = the.ClientList[request.Serveraddress]
	}

	var exist bool
	if err = the.Dbcontrol.Model(db.Client{}).Select(`COUNT(*) > 0`).Where(db.Client{Serveraddress: request.Serveraddress}).Scan(&exist).Error; err != nil {
		return err
	}

	var clientconfiguration = new(db.Client)
	if exist {
		if err = the.Dbcontrol.Where(db.Client{Serveraddress: request.Serveraddress}).First(clientconfiguration).Error; err != nil {
			return err
		}
	} else {
		clientconfiguration = &db.Client{
			Serveraddress:    request.Serveraddress,
			Maxretentiondays: request.Maxretentiondays,
			Reload:           true,
			Status:           "Online",
			Store:            make([]db.Clientmodule, 0),
		}

		if err = the.Dbcontrol.Create(clientconfiguration).Error; err != nil {
			return err
		}

		log.Printf("Create new client ----> %s\r\n", request.Serveraddress)
	}

	log.Printf("GRPC Client connect ----> %s\r\n", request.Serveraddress)

	// 变更Client状态
	if err = the.Dbcontrol.Model(db.Client{}).Where(db.Client{Serveraddress: request.Serveraddress}).Update("status", "online").Error; err != nil {
		log.Printf("Failed to Change Client Status: %s\r\n", request.Serveraddress)
	}

	// 如果客户端传递的配置与数据库中的不一致则更新数据库
	if clientconfiguration.Maxretentiondays != request.Maxretentiondays {
		if err = the.Dbcontrol.Model(db.Client{}).Where(db.Client{Serveraddress: request.Serveraddress}).Updates(map[string]interface{}{
			"maxretentiondays": request.Maxretentiondays,
			"reload":           false,
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
				log.Printf("Client %s disconnected\r\n", request.Serveraddress)
				if err = the.Dbcontrol.Model(db.Client{}).Where(db.Client{Serveraddress: request.Serveraddress}).Update("status", "offline").Error; err != nil {
					log.Printf("Failed to Change Client Status: %s\r\n", request.Serveraddress)
				}
				return
			default:
				select {
				case messagechannel <- &rpc.ExpirationPushResponse{Heartbeat: request.Serveraddress}:
				default:
					log.Printf("Client %s channel is full, dropping message", request.Serveraddress)
				}
			}
		}
	}()

	// 发送Delete信息
	for {
		select {
		case message := <-messagechannel:
			if strings.EqualFold(message.Heartbeat, "") {
				the.Logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("Expirationforclient: Partnumber(%s) Modules(%s)", message.Partnumber, message.Modulename))
			}

			if err := stream.Send(message); err != nil {
				the.Logmar.GetLogger("Expirationforclient").Error("failed to send expiration message to client: ", err.Error())
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

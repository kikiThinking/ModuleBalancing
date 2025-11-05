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
	"log"
	"sync"
	"time"

	"golang.org/x/net/context"
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
	//var messagechannel chan *rpc.ExpirationPushResponse

	// 首次连接
	//if _, ok := the.ClientList[request.Serveraddress]; !ok {
	//	the.ClientList[request.Serveraddress] = make(chan *rpc.ExpirationPushResponse, 10)
	//
	//	messagechannel = the.ClientList[request.Serveraddress]
	//} else {
	//	messagechannel = the.ClientList[request.Serveraddress]
	//}
	globalctx, globalcancel := context.WithCancel(stream.Context())
	defer globalcancel()

	the.mu.Lock()
	messagechannel, exists := the.ClientList[request.Serveraddress]
	if !exists {
		messagechannel = make(chan *rpc.ExpirationPushResponse, 10)
		the.ClientList[request.Serveraddress] = messagechannel
	}
	the.mu.Unlock()

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

		log.Printf("Create new clientcontrol ----> %s\r\n", request.Serveraddress)
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

		for {
			select {
			case <-globalctx.Done(): // 使用统一的 ctx
				log.Printf("Client %s heartbeat stopped\r\n", request.Serveraddress)
				return
			case <-ticker.C:
				select {
				case <-globalctx.Done():
					return
				case messagechannel <- &rpc.ExpirationPushResponse{Heartbeat: request.Serveraddress}:
					// 心跳发送成功
				default:
					log.Printf("Client %s channel is full, dropping message", request.Serveraddress)
				}
			}
		}
	}()

	// 发送Delete信息
	for {
		select {
		case <-globalctx.Done(): // 使用统一的 ctx
			return nil

		case message, ok := <-messagechannel:
			if !ok {
				// Channel 被关闭，清理客户端
				log.Printf("Client %s message channel closed", request.Serveraddress)
				return nil
			}

			// 安全地处理消息
			if err := stream.Send(message); err != nil {
				the.Logmar.GetLogger("Expirationforclient").Error("failed to send expiration message to clientcontrol: ", err.Error())
				the.BreakClient(request.Serveraddress)
				log.Printf("Failed to send message to clientcontrol %s: %v", request.Serveraddress, err)
				return err
			}
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

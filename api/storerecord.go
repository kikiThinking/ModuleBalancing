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
	"errors"
	"fmt"
	"gorm.io/gorm"
	"io"
	"strings"
	"time"
)

type Storerecord struct {
	Dbcontrol *gorm.DB
	Logmar    *logmanager.LogManager
	rpc.UnimplementedStorerecordServer
}

// Updatestorerecord 函数接收客户端即将使用的Module列表, 也就是AOD解析出来的结果, 服务端接收这些结果用来记录过期时间通知客户端删除
func (the *Storerecord) Updatestorerecord(stream rpc.Storerecord_UpdatestorerecordServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				err := stream.SendAndClose(&rpc.StorerecordResponse{})
				if err != nil {
					// 记录错误日志
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Error sending and closing stream: %s", err.Error()))
				}
				return nil
			} else {
				// 记录错误日志
				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Error receiving stream: %s", err.Error()))
				return err
			}
		}

		// 如果心跳为空则证明服务端发送的是业务数据
		if strings.EqualFold(req.Heartbeat, "") {
			// 开启事务
			tx := the.Dbcontrol.Begin()

			var client = new(db.Client)
			if err = the.Dbcontrol.Preload(`Store`).Where(db.Client{Serveraddress: req.Serveraddress}).First(client).Error; err != nil {
				tx.Rollback() // 查询失败，回滚事务
				if errors.Is(err, gorm.ErrRecordNotFound) {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Unregistered clients(%s)", req.Serveraddress))
					return err
				} else {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
					return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
				}
			}

			// 客户端文件超时逻辑代码
			for _, value := range req.Modulenames {
				found := false // 标记是否找到匹配的模块
				for j := range client.Store {
					if strings.EqualFold(client.Store[j].Name, value) {
						found = true
						client.Store[j].Partnumber = req.Partnumber
						//client.Store[j].Expiration = time.Now().Add(time.Hour * 24 * time.Duration(client.Maxretentiondays))
						client.Store[j].Expiration = time.Now().Add(time.Duration(client.Maxretentiondays) * time.Hour * 24)

						// 这里如果找到记录那就更新一下当前的AOD名称和过时间
						if err := tx.Model(&client.Store[j]).UpdateColumns(map[string]interface{}{
							"partnumber": req.Partnumber,
							"expiration": client.Store[j].Expiration,
						}).Error; err != nil {
							tx.Rollback()
							the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
							return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
						}
						break // 找到匹配的模块后，跳出内层循环
					}
				}

				// 没有找到对应的模块，创建新的 Clientmodule 记录
				if !found {
					module := db.Clientmodule{
						StoreID:    client.ID,      // 关联到当前的 Client
						Name:       value,          // 使用 req.Modulenames 中的 value 作为 Name
						Partnumber: req.Partnumber, // 使用 req.Partnumber
						Expiration: time.Now().Add(time.Duration(client.Maxretentiondays) * time.Hour * 24),
					}

					if err := tx.Create(&module).Error; err != nil {
						tx.Rollback()
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
						return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
					}

					client.Store = append(client.Store, module)
					the.Logmar.GetLogger("Clientstore").Info(fmt.Sprintf("Add a new client storage record --> Client(%s)  Partnumber(%s)  Filename(%s)  Expiration(%s)",
						req.Serveraddress,
						module.Partnumber,
						module.Name,
						module.Expiration.Format("2006-01-02 15:04:05"),
					))
				}
			}

			if err := tx.Commit().Error; err != nil {
				tx.Rollback()
				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
			}

			if r := recover(); r != nil {
				tx.Rollback()
				panic(r) // 重新抛出panic
			}
		}
	}
}

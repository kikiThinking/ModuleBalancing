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
	"ModuleBalancing/env"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"errors"
	"fmt"
	"io"
	"time"

	"gorm.io/gorm"
)

type Storerecord struct {
	Dbcontrol     *gorm.DB
	Configuration *env.Configuration
	Logmar        *logmanager.LogManager
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
			}

			// 记录错误日志
			the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Error receiving stream: %s", err.Error()))
			return err
		}

		// 如果心跳为空则证明服务端发送的是业务数据
		if req.Heartbeat == "" {
			// 开启事务
			var (
				tx          = the.Dbcontrol.Begin()
				client      = new(db.Client)
				currenttime = time.Now()
			)

			if err = tx.Where(db.Client{Serveraddress: req.Serveraddress}).First(client).Error; err != nil {
				tx.Rollback() // 查询失败，回滚事务
				if errors.Is(err, gorm.ErrRecordNotFound) {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Unregistered clients(%s)", req.Serveraddress))
					return err
				}

				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
			}

			// 客户端文件超时逻辑代码
			for _, value := range req.Modulenames {

				// 这段代码用于在客户端更新store时同时更新服务端本地的record
				// 服务端Module更新 这里允许Module不存在的情况 因为他可能是Normal的Module
				// ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
				var module db.Module
				if err = tx.Where(db.Module{Name: value}).First(&module).Error; err != nil {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
				} else {
					if err = tx.Model(&module).Updates(map[string]interface{}{
						`lastuse`:    currenttime,
						`expiration`: currenttime.Add(time.Duration(the.Configuration.Setting.Expiration) * time.Hour * 24),
					}).Error; err != nil {
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
					}
				}
				// ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

				var clientmodule = db.Clientmodule{
					StoreID:    client.ID,
					Partnumber: req.Partnumber,
					Name:       value,
					Expiration: currenttime.Add(time.Duration(client.Maxretentiondays) * time.Hour * 24),
				}

				var record db.Clientmodule
				if err = tx.Unscoped().
					Where(db.Clientmodule{StoreID: clientmodule.StoreID, Name: value}).
					First(&record).Error; err != nil {
					if errors.Is(err, gorm.ErrRecordNotFound) {
						if err = tx.Model(db.Clientmodule{}).Create(&clientmodule).Error; err != nil {
							tx.Rollback()
							the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
							return err
						}
					} else {
						tx.Rollback()
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
						return err
					}
				} else {
					updates := map[string]interface{}{
						"partnumber": clientmodule.Partnumber,
						"expiration": clientmodule.Expiration,
						"updated_at": currenttime,
					}
					if record.DeletedAt.Valid {
						updates["deleted_at"] = nil
					}

					if err = tx.Unscoped().
						Model(&record).
						Updates(updates).Error; err != nil {
						tx.Rollback()
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
						return err
					}
				}

				the.Logmar.GetLogger("Clientstore").Info(fmt.Sprintf("Add clientcontrol storage record --> Client(%s)  Partnumber(%s)  Filename(%s)  Expiration(%s)",
					req.Serveraddress,
					clientmodule.Partnumber,
					clientmodule.Name,
					clientmodule.Expiration.Format("2006-01-02 15:04:05"),
				))
			}

			if err = tx.Commit().Error; err != nil {
				tx.Rollback()
				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
			}
		}
	}
}

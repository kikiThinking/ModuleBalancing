/*
*

	@author: kiki
	@since: 2025/9/10
	@desc: //TODO

*
*/

package route

import (
	"ModuleBalancing/db"
	"ModuleBalancing/logmanager"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type GinRoutes struct {
	DBConnect   *gorm.DB
	Logwri      *logmanager.BusinessLogger
	Serverstart string
	//ClientUpdateControl *clientcontrol.ClientControl
}

type UnifiedResponse struct {
	Status string `json:"status"`
	Result any    `json:"result"`
}

func (the *GinRoutes) InformationCollection(engine *gin.Engine) *gin.Engine {
	var group = engine.Group("collect")
	group.GET("/serverstart", func(ctx *gin.Context) {
		ctx.JSON(http.StatusOK, UnifiedResponse{
			Status: "ok",
			Result: the.Serverstart,
		})
	})

	//group.GET("/version", func(ctx *gin.Context) {
	//	ctx.JSON(http.StatusOK, UnifiedResponse{
	//		Status: "ok",
	//		Result: the.ClientUpdateControl.MD5(),
	//	})
	//})
	//
	//group.GET("client", func(ctx *gin.Context) {
	//	ctx.Header("Content-Type", "application/octet-stream")
	//	ctx.Header("Content-Disposition", "attachment; filename="+url.QueryEscape("Modulebalancingclient.exe"))
	//	ctx.Header("Content-Transfer-Encoding", "binary")
	//	ctx.Header("Content-Length", strconv.Itoa(len(the.ClientUpdateControl.Data()))) // 关键：添加 Content-Length
	//
	//	// 添加缓存控制头部
	//	ctx.Header("Cache-Control", "no-cache, no-store, must-revalidate")
	//	ctx.Header("Pragma", "no-cache")
	//	ctx.Header("Expires", "0")
	//
	//	ctx.Data(http.StatusOK, "application/octet-stream", the.ClientUpdateControl.Data())
	//})

	group.GET("/clientonlinelist", func(ctx *gin.Context) {
		var response = make([]map[string]interface{}, 0)
		if err := the.DBConnect.WithContext(ctx).Model(db.Client{}).
			Select("serveraddress, maxretentiondays, accumulate_download, status, `group`").
			Find(&response).Error; err != nil {
			ctx.JSON(http.StatusBadRequest, UnifiedResponse{
				Status: err.Error(),
				Result: nil,
			})
			return
		}

		ctx.JSON(http.StatusOK, UnifiedResponse{
			Status: "ok",
			Result: response,
		})
	})

	// by day获取当月即将过期的module总大小
	group.GET("/clientexpirationlist/:device", func(ctx *gin.Context) {
		var (
			loop     = time.Date(time.Now().Year(), time.Now().Month()+1, 0, 0, 0, 0, 0, time.Now().Location()).Day()
			response = make([]float64, 0, loop)
			device   = ctx.Param("device")
		)
		for i := 1; i <= loop; i++ {
			var (
				startofday = time.Date(time.Now().Year(), time.Now().Month(), i, 0, 0, 0, 0, time.Now().Location())
				size       float64
			)

			switch device {
			case "clientcontrol":
				if err := the.DBConnect.WithContext(ctx).Table("clientmodules as t1").
					Joins("INNER JOIN modules as t2 ON t1.name = t2.name").
					Where("t1.expiration >= ? AND t1.expiration < ? AND t1.deleted_at is null", startofday.Format("2006-01-02 15:04:05"), startofday.Add(time.Hour*24).Format("2006-01-02 15:04:05")).
					Select("COALESCE(SUM(t2.size), 0)").Scan(&size).Error; err != nil {
					ctx.JSON(http.StatusBadRequest, UnifiedResponse{
						Status: err.Error(),
						Result: nil,
					})
					return
				}
			case "server":
				if err := the.DBConnect.WithContext(ctx).Table("modules").
					Where("expiration >= ? AND expiration < ?  AND deleted_at is null", startofday.Format("2006-01-02 15:04:05"), startofday.Add(time.Hour*24).Format("2006-01-02 15:04:05")).
					Select("COALESCE(SUM(size), 0)").Scan(&size).Error; err != nil {
					ctx.JSON(http.StatusBadRequest, UnifiedResponse{
						Status: err.Error(),
						Result: nil,
					})
					return
				}
			}
			response = append(response, math.Round(size/(1024*1024*1024)*math.Pow10(2))/math.Pow10(2))

		}

		ctx.JSON(http.StatusOK, UnifiedResponse{
			Status: "ok",
			Result: response,
		})
	})

	group.GET("/clientmodulelist/:address", func(ctx *gin.Context) {
		var (
			address  = ctx.Param("address")
			response = make([]map[string]interface{}, 0)
		)

		if err := the.DBConnect.WithContext(ctx).Table("clients").
			Joins("INNER JOIN clientmodules ON clientmodules.store_id = clients.id").
			Joins("INNER JOIN modules ON modules.name = clientmodules.name").
			Where("clients.serveraddress = ?  AND clientmodules.deleted_at is null", address).
			Select("clientmodules.created_at, clientmodules.partnumber, clientmodules.name, modules.size, clientmodules.expiration").
			Scan(&response).Error; err != nil {
			ctx.JSON(http.StatusBadRequest, UnifiedResponse{
				Status: err.Error(),
				Result: nil,
			})
			return
		}

		ctx.JSON(http.StatusOK, UnifiedResponse{
			Status: "ok",
			Result: response,
		})

	})

	group.GET("/clientsexpirationaccumulated", func(ctx *gin.Context) {
		var (
			clientaddress = make([]string, 0)
			response      = make([]map[string]interface{}, 0)
		)

		if err := the.DBConnect.WithContext(ctx).Model(db.Client{}).Select("serveraddress").Find(&clientaddress).Error; err != nil {
			ctx.JSON(http.StatusBadRequest, UnifiedResponse{
				Status: err.Error(),
				Result: nil,
			})
			return
		}

		for _, val := range clientaddress {
			var clientresult = make([]map[string]interface{}, 0)
			if err := the.DBConnect.WithContext(ctx).Table("clients").
				Joins("INNER JOIN clientmodules ON clientmodules.store_id = clients.id").
				Joins("INNER JOIN modules ON modules.name = clientmodules.name").
				Where("clients.serveraddress = ? AND clientmodules.deleted_at is null", val).
				Select("clients.serveraddress, clientmodules.created_at, clientmodules.partnumber, clientmodules.name, modules.size, clientmodules.expiration").
				Scan(&clientresult).Error; err != nil {
				ctx.JSON(http.StatusBadRequest, UnifiedResponse{
					Status: err.Error(),
					Result: nil,
				})
				return
			}
			response = append(response, clientresult...)
		}

		sort.Slice(response, func(i, j int) bool {
			return response[i]["expiration"].(time.Time).Before(response[j]["expiration"].(time.Time))
		})

		ctx.JSON(http.StatusOK, UnifiedResponse{
			Status: "ok",
			Result: response,
		})

	})

	return engine
}

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
	"bufio"
	"bytes"
	"fmt"
	"gorm.io/gorm"
	"hash/crc64"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
)

type Configuration struct {
	Setting struct {
		Expiration      int64  `yaml:"Expiration"`
		CheckExpiration int64  `yaml:"CheckExpiration"`
		Common          string `yaml:"Common"`
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
	Backup struct {
		Host string `yaml:"Host"`
		Port string `yaml:"Port"`
	} `yaml:"Backup"`
}

func CRC64(filePath string, chunkSize int64, workers int) (uint64, int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("filed to get file information: %v", err)
	}

	fileSize := fileInfo.Size()
	if fileSize == 0 {
		return 0, fileInfo.Size(), err
	}

	chunks := int((fileSize + chunkSize - 1) / chunkSize)
	if workers > chunks {
		workers = chunks
	}

	table := crc64.MakeTable(crc64.ECMA)
	var wg sync.WaitGroup
	results := make([]uint64, chunks) // 使用切片存储结果，保持顺序
	workCh := make(chan int, chunks)
	errCh := make(chan error, 1)
	var hasError bool
	var mu sync.Mutex // 用于保护hasError

	// 启动worker池
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
				n, err := file.ReadAt(buf, offset)
				if err != nil && err != io.EOF {
					mu.Lock()
					if !hasError {
						errCh <- fmt.Errorf("读取文件块 %d 失败: %v", chunkIndex, err)
						hasError = true
					}
					mu.Unlock()
					return
				}

				// 计算当前块的CRC
				sum := crc64.Checksum(buf[:n], table)

				// 直接存储到结果切片的对应位置
				results[chunkIndex] = sum
			}
		}()
	}

	// 分发任务
	go func() {
		for i := 0; i < chunks; i++ {
			mu.Lock()
			if hasError {
				mu.Unlock()
				break
			}
			mu.Unlock()
			workCh <- i
		}
		close(workCh)
	}()

	// 等待完成
	go func() {
		wg.Wait()
		close(errCh) // 关闭错误通道表示所有工作完成
	}()

	// 处理错误
	if err := <-errCh; err != nil {
		return 0, fileInfo.Size(), err
	}

	// 按顺序合并所有块的CRC值
	finalCRC := uint64(0)
	for i := 0; i < chunks; i++ {
		sum := results[i]
		finalCRC = crc64.Update(finalCRC, table, []byte{
			byte(sum >> 56), byte(sum >> 48), byte(sum >> 40), byte(sum >> 32),
			byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
		})
	}

	return finalCRC, fileInfo.Size(), nil
}

func Analyzing(ctx *gorm.DB, common string, source []byte) ([]string, []string, error) {
	var (
		crifilelist    = make([]string, 0)
		response       = make([]string, 0)
		analyzingerror = make([]string, 0)
		buf            = bufio.NewScanner(bytes.NewReader(source))
	)

	matchstr, err := regexp.Compile(`(?i)FILE=.*CRI`) //
	if err != nil {
		return nil, nil, err
	}

	for buf.Scan() {
		if buf.Err() != nil {
			break
		}

		var crifilename = strings.NewReplacer("FILE=", "", "File=", "").Replace(matchstr.FindString(buf.Text()))
		if strings.EqualFold(crifilename, "") {
			continue
		}

		crifilelist = append(crifilelist, strings.TrimSpace(crifilename))
	}

	matchstr, err = regexp.Compile(`(?i)modulename\d*.*`) //
	if err != nil {
		return nil, nil, err
	}

	for _, value := range crifilelist {
		var filename string
		if err = ctx.Model(db.Module{}).Select(`name`).Where(db.Module{Name: value}).First(&filename).Error; err != nil {
			if _, exist := os.Stat(strings.Join([]string{common, value}, `\`)); os.IsNotExist(exist) {
				fmt.Printf("实体文件不存在, 去Backup Server Copy")
				analyzingerror = append(analyzingerror, value)
				continue
				// 实体文件不存在 去Backup Server Copy并更新Lastuse, Expiration
			}
		}

		response = append(response, strings.TrimSpace(value))
		f, err := os.ReadFile(strings.Join([]string{common, value}, `\`))
		if err != nil {
			analyzingerror = append(analyzingerror, value)
			continue
		}

		buf = bufio.NewScanner(bytes.NewBuffer(f))

		for buf.Scan() {
			if buf.Err() != nil {
				break
			}

			var modulename = matchstr.FindString(buf.Text())
			if strings.EqualFold(modulename, "") || len(strings.Split(modulename, "=")) != 2 {
				continue
			}

			response = append(response, strings.TrimSpace(strings.Split(modulename, "=")[1]))
		}
	}

	return response, analyzingerror, nil
}

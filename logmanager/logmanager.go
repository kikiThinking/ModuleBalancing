package logmanager

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogLevel 日志级别类型
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
	FATAL
)

var levelNames = map[LogLevel]string{
	DEBUG: "DEBUG",
	INFO:  "INFO",
	WARN:  "WARN",
	ERROR: "ERROR",
	FATAL: "FATAL",
}

// LoggerConfig 日志配置
type LoggerConfig struct {
	BusinessName string    // 业务名称
	LogDir       string    // 日志目录
	MaxSize      int64     // 单个日志文件最大大小 (MB)
	MaxBackups   int       // 保留的旧日志文件数量
	MinLevel     LogLevel  // 最小日志级别
	RotatePeriod time.Time // 日志轮转时间
}

// LogManager 日志管理器
type LogManager struct {
	loggers   map[string]*BusinessLogger
	configs   map[string]LoggerConfig
	lock      sync.RWMutex
	closeChan chan struct{}
	wg        sync.WaitGroup
}

// BusinessLogger 业务日志记录器
type BusinessLogger struct {
	business string
	file     *os.File
	writer   *os.File
	config   LoggerConfig
	lock     sync.Mutex
	queue    chan string
	stopChan chan struct{}
	wg       sync.WaitGroup
}

// 全局日志管理器实例
var manager *LogManager
var once sync.Once

// InitManager 初始化日志管理器
func InitManager() *LogManager {
	once.Do(func() {
		manager = &LogManager{
			loggers:   make(map[string]*BusinessLogger),
			configs:   make(map[string]LoggerConfig),
			closeChan: make(chan struct{}),
		}

		// 启动日志轮转检查协程
		go manager.rotationChecker()
	})
	return manager
}

// RegisterBusiness 注册业务日志配置
func (m *LogManager) RegisterBusiness(config LoggerConfig) {
	m.lock.Lock()
	defer m.lock.Unlock()

	m.configs[config.BusinessName] = config
}

// GetLogger 获取业务日志记录器
func (m *LogManager) GetLogger(business string) *BusinessLogger {
	m.lock.Lock()
	defer m.lock.Unlock()

	if logger, exists := m.loggers[business]; exists {
		return logger
	}

	// 如果业务未注册，使用默认配置
	config, exists := m.configs[business]
	if !exists {
		config = LoggerConfig{
			BusinessName: business,
			LogDir:       "./logs",
			MaxSize:      100, // 100MB
			MaxBackups:   7,   // 保留7天
			MinLevel:     INFO,
		}
	}

	// 创建新日志记录器
	logger := &BusinessLogger{
		business: business,
		config:   config,
		queue:    make(chan string, 10000), // 缓冲队列
		stopChan: make(chan struct{}),
	}

	// 初始化日志文件
	if err := logger.initLogFile(); err != nil {
		fmt.Printf("初始化日志文件失败: %v\n", err)
		// 使用标准输出作为后备
		logger.writer = os.Stdout
	}

	// 启动日志处理协程
	logger.wg.Add(1)
	go logger.processLogs()

	m.loggers[business] = logger
	return logger
}

// Close 关闭所有日志记录器
func (m *LogManager) Close() {
	close(m.closeChan)

	m.lock.Lock()
	defer m.lock.Unlock()

	// 关闭所有业务日志记录器
	for business, logger := range m.loggers {
		logger.stop()
		delete(m.loggers, business)
	}

	// 等待所有日志写入完成
	m.wg.Wait()
}

// 日志轮转检查
func (m *LogManager) rotationChecker() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.checkRotations()
		case <-m.closeChan:
			return
		}
	}
}

// 检查所有业务日志是否需要轮转
func (m *LogManager) checkRotations() {
	m.lock.RLock()
	defer m.lock.RUnlock()

	now := time.Now()
	for _, logger := range m.loggers {
		// 检查时间轮转
		if !logger.config.RotatePeriod.IsZero() && now.After(logger.config.RotatePeriod) {
			logger.rotateByTime()
		}

		// 检查大小轮转
		logger.rotateBySize()
	}
}

// 初始化日志文件
func (l *BusinessLogger) initLogFile() error {
	l.lock.Lock()
	defer l.lock.Unlock()

	// 确保日志目录存在
	if err := os.MkdirAll(l.config.LogDir, 0755); err != nil {
		return err
	}

	// 创建日志文件
	logPath := filepath.Join(l.config.LogDir, fmt.Sprintf("%s.log", l.business))
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	// 关闭旧文件（如果存在）
	if l.file != nil {
		l.file.Close()
	}

	l.file = file
	l.writer = file
	return nil
}

// 处理日志
func (l *BusinessLogger) processLogs() {
	defer l.wg.Done()

	for {
		select {
		case msg := <-l.queue:
			l.writeLog(msg)
		case <-l.stopChan:
			// 处理队列中剩余日志
			for len(l.queue) > 0 {
				msg := <-l.queue
				l.writeLog(msg)
			}
			return
		}
	}
}

// 写入日志到文件
func (l *BusinessLogger) writeLog(msg string) {
	l.lock.Lock()
	defer l.lock.Unlock()

	if l.writer == nil {
		return
	}

	if _, err := l.writer.WriteString(msg + "\n"); err != nil {
		fmt.Printf("写入日志失败: %v\n", err)
	}
}

// 停止日志记录器
func (l *BusinessLogger) stop() {
	close(l.stopChan)
	l.wg.Wait()

	l.lock.Lock()
	defer l.lock.Unlock()

	if l.file != nil {
		l.file.Close()
		l.file = nil
		l.writer = nil
	}
}

// 根据大小轮转日志
func (l *BusinessLogger) rotateBySize() {
	l.lock.Lock()
	defer l.lock.Unlock()

	if l.file == nil {
		return
	}

	// 获取文件信息
	fileInfo, err := l.file.Stat()
	if err != nil {
		return
	}

	// 检查文件大小
	if fileInfo.Size() < l.config.MaxSize*1024*1024 {
		return
	}

	// 执行轮转
	l.rotateLogFile()
}

// 根据时间轮转日志
func (l *BusinessLogger) rotateByTime() {
	l.lock.Lock()
	defer l.lock.Unlock()
	l.rotateLogFile()

	// 设置下一次轮转时间
	l.config.RotatePeriod = time.Now().Add(24 * time.Hour)
}

// 轮转日志文件
func (l *BusinessLogger) rotateLogFile() {
	if l.file == nil {
		return
	}

	// 关闭当前文件
	l.file.Close()

	// 创建新文件名（带时间戳）
	timestamp := time.Now().Format("20060102-150405")
	oldPath := filepath.Join(l.config.LogDir, fmt.Sprintf("%s.log", l.business))
	newPath := filepath.Join(l.config.LogDir, fmt.Sprintf("%s-%s.log", l.business, timestamp))

	// 重命名文件
	_ = os.Rename(oldPath, newPath)

	// 重新打开日志文件
	file, err := os.OpenFile(oldPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Printf("轮转后重新打开日志文件失败: %v\n", err)
		l.writer = os.Stdout
		return
	}

	l.file = file
	l.writer = file

	// 清理旧日志文件
	l.cleanOldLogs()
}

// 清理旧日志文件
func (l *BusinessLogger) cleanOldLogs() {
	files, err := filepath.Glob(filepath.Join(l.config.LogDir, fmt.Sprintf("%s-*.log", l.business)))
	if err != nil {
		return
	}

	// 按修改时间排序
	sortByModTime(files)

	// 删除超出保留数量的文件
	if len(files) > l.config.MaxBackups {
		for i := 0; i < len(files)-l.config.MaxBackups; i++ {
			_ = os.Remove(files[i])
		}
	}
}

// Log 日志记录方法
func (l *BusinessLogger) Log(level LogLevel, format string, args ...interface{}) {
	if level < l.config.MinLevel {
		return
	}

	msg := fmt.Sprintf("[%s] [%s] %s",
		time.Now().Format("2006-01-02 15:04:05.000"),
		levelNames[level],
		fmt.Sprintf(format, args...))

	select {
	case l.queue <- msg: // 将日志放入队列
	default:
		// 队列满时丢弃日志（或根据需要调整策略）
		fmt.Println("日志队列已满，丢弃日志:", msg)
	}
}

// Debug 快捷方法
func (l *BusinessLogger) Debug(format string, args ...interface{}) {
	l.Log(DEBUG, format, args...)
}

func (l *BusinessLogger) Info(format string, args ...interface{}) {
	l.Log(INFO, format, args...)
}

func (l *BusinessLogger) Warn(format string, args ...interface{}) {
	l.Log(WARN, format, args...)
}

func (l *BusinessLogger) Error(format string, args ...interface{}) {
	l.Log(ERROR, format, args...)
}

func (l *BusinessLogger) Fatal(format string, args ...interface{}) {
	l.Log(FATAL, format, args...)
	os.Exit(1)
}

// 辅助函数：按修改时间排序文件
func sortByModTime(files []string) {
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			infoI, _ := os.Stat(files[i])
			infoJ, _ := os.Stat(files[j])
			if infoI.ModTime().After(infoJ.ModTime()) {
				files[i], files[j] = files[j], files[i]
			}
		}
	}
}

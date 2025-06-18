package master

import (
	. "biliTickerStorm/internal/common"
	masterpb "biliTickerStorm/internal/master/pb"
	workerpb "biliTickerStorm/internal/worker/pb"
	"context"
	"fmt"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var log = logrus.New()

// Worker 工作节点信息
type Worker struct {
	WorkerID     string
	Address      string
	Status       WorkerStatus
	TaskAssigned string
	UpdateTime   time.Time //心跳
}

// Server 服务器结构
type Server struct {
	masterpb.UnimplementedTicketMasterServer
	workers    map[string]*Worker
	workersMux sync.RWMutex
	logger     *logrus.Logger
	// 任务管理
	tasks    map[string]*TaskInfo
	tasksMux sync.RWMutex
	// 配置
	heartbeatTimeout time.Duration
	taskTimeout      time.Duration
	maxRetries       int
	// 停止信号
	stopChan        chan struct{}
	scheduleTrigger chan struct{} // 🔔 调度触发通道
}

// NewServer 创建新的服务器实例
func NewServer() *Server {
	server := &Server{
		workers:          make(map[string]*Worker),
		logger:           logrus.New(),
		tasks:            make(map[string]*TaskInfo),
		heartbeatTimeout: 10 * time.Second, //
		taskTimeout:      30 * time.Second, //
		maxRetries:       3,
		stopChan:         make(chan struct{}),
		scheduleTrigger:  make(chan struct{}, 1),
	}

	go server.startHeartbeatChecker()
	go server.startTaskScheduler()
	go server.startTaskMonitor()

	return server

}

func (s *Server) LoadTasksFromDir(dirPath string) error {
	files, err := ioutil.ReadDir(dirPath)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}
		if strings.HasSuffix(file.Name(), ".json") {
			fullPath := filepath.Join(dirPath, file.Name())
			content, err := os.ReadFile(fullPath)
			if err != nil {
				log.Printf("Failed to read file %s: %v", fullPath, err)
				continue
			}
			taskName := strings.TrimSuffix(file.Name(), ".json")
			tickerConfigContent := string(content)
			task := s.CreateTask(taskName, tickerConfigContent)
			log.Printf("Loaded task from file: %s => Task ID: %s", file.Name(), task.ID)
		}
	}

	return nil
}

func (s *Server) CancelTask(ctx context.Context, req *masterpb.CancelTaskInfo) (*masterpb.CancelReply, error) {
	s.workersMux.Lock()
	s.tasksMux.Lock()
	defer s.workersMux.Unlock()
	defer s.tasksMux.Unlock()
	cancelTask, exists := s.tasks[req.CancelTaskId]
	if !exists {
		return nil, fmt.Errorf("<%s> not found", req.CancelTaskId)
	}
	if cancelTask.AssignedTo != req.WorkerId {
		return nil, fmt.Errorf("<%s> not own by <%s>", req.CancelTaskId, req.WorkerId)
	}
	ownWorkerId := req.WorkerId
	s.workers[ownWorkerId].TaskAssigned = ""
	s.workers[ownWorkerId].Status = Idle
	s.workers[ownWorkerId].UpdateTime = time.Now()
	return &masterpb.CancelReply{
		Success: true,
		Message: fmt.Sprintf("<%s> cancel <%s> Successfully.", req.WorkerId, req.CancelTaskId),
	}, nil
}

func (s *Server) RegisterWorker(ctx context.Context, req *masterpb.WorkerInfo) (*masterpb.RegisterReply, error) {
	s.workersMux.Lock()
	s.tasksMux.Lock()
	defer s.tasksMux.Unlock()
	defer s.workersMux.Unlock()
	defer s.triggerSchedule()
	existingWorker, exists := s.workers[req.WorkerId]
	if exists {
		existingWorker.Address = req.Address
		if existingWorker.Status != WorkerStatus(req.WorkStatus) {
			existingWorker.Status = WorkerStatus(req.WorkStatus)
			s.triggerSchedule() //触发调度
		}
		existingWorker.TaskAssigned = req.TaskAssigned
		existingWorker.UpdateTime = time.Now()

		if req.TaskAssigned != "" {
			task, exists := s.tasks[req.TaskAssigned]
			if !exists {
				return nil, fmt.Errorf("<%s> not found", req.TaskAssigned)
			}
			if string(task.Status) != req.TaskStatus {
				//task信息发生变化
				oldStatus := task.Status
				task.Status = TaskStatus(req.TaskStatus)
				log.Printf("<%s> => <%s>: %s ", oldStatus, task.Status, task.TaskName)
				s.triggerSchedule() //触发调度
			}
			task.UpdatedAt = time.Now() //心跳信息
		}
		return &masterpb.RegisterReply{
			Success: true,
			Message: "Worker Update Successfully",
		}, nil
	}
	newWorker := &Worker{
		WorkerID:     req.WorkerId,
		Address:      req.Address,
		Status:       WorkerStatus(req.WorkStatus),
		TaskAssigned: req.TaskAssigned,
		UpdateTime:   time.Now(),
	}
	s.workers[req.WorkerId] = newWorker
	s.logger.Infof("Worker Register: ID=%s, Address=%s, WorkStatus=%s",
		req.WorkerId, req.Address, WorkerStatus(req.WorkStatus).String())
	return &masterpb.RegisterReply{
		Success: true,
		Message: "Worker Register Successfully",
	}, nil
}

// 心跳检查器
func (s *Server) startHeartbeatChecker() {
	ticker := time.NewTicker(5 * time.Second) // 每5秒检查一次
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.checkWorkerHeartbeats()
		case <-s.stopChan:
			return
		}
	}
}

func (s *Server) Stop() {
	close(s.stopChan)
	log.Println("Master Stopped")
}

func (s *Server) CreateTask(taskName, tickerConfigContent string) *TaskInfo {
	s.tasksMux.Lock()
	defer s.tasksMux.Unlock()
	defer s.triggerSchedule()

	taskID := fmt.Sprintf("task-%d", time.Now().UnixNano())
	task := &TaskInfo{
		ID:                  taskID,
		Status:              TaskStatusPending,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
		TaskName:            taskName,
		TickerConfigContent: tickerConfigContent,
	}

	s.tasks[taskID] = task
	log.Printf("Create Task : ID=%s, name=%s", taskID, taskName)
	return task
}

func (s *Server) checkWorkerHeartbeats() {
	s.workersMux.Lock()
	defer s.workersMux.Unlock()

	now := time.Now()
	offlineWorkers := make([]string, 0)

	for workerID, worker := range s.workers {
		if now.Sub(worker.UpdateTime) > s.heartbeatTimeout {
			log.Printf("Worker %s 心跳超时，标记为离线", workerID)
			worker.Status = Down
			offlineWorkers = append(offlineWorkers, workerID)
			if worker.TaskAssigned != "" {
				log.Printf("Worker %s 所持有的任务 %s 标记为pending", workerID, worker.TaskAssigned)
				s.tasksMux.Lock()
				s.clearAndPendingTask(s.tasks[worker.TaskAssigned]) //重新分配
				s.tasksMux.Unlock()
				s.triggerSchedule() //离线触发调度
			}
		}
	}

	// 清理离线worker
	for _, workerID := range offlineWorkers {
		delete(s.workers, workerID)
	}
}
func (s *Server) triggerSchedule() {
	select {
	case s.scheduleTrigger <- struct{}{}:
	default:
		// 排队跳过
	}
}

// 任务调度器
func (s *Server) startTaskScheduler() {
	for {
		select {
		case <-s.scheduleTrigger:
			s.scheduleTasks()
		case <-s.stopChan:
			return
		}
	}
}

func (s *Server) scheduleTasks() {
	s.tasksMux.Lock()
	s.workersMux.RLock()
	idleWorkers := make([]*Worker, 0)
	for _, worker := range s.workers {
		if worker.Status == Idle {
			idleWorkers = append(idleWorkers, worker)
		}
	}

	pendingTasks := make([]*TaskInfo, 0) //需要分配的task
	for _, task := range s.tasks {
		if task.Status == TaskStatusPending { //过滤一下，保证s.taskQueue 里面都是pendingTasks
			pendingTasks = append(pendingTasks, task)
		}
	}
	s.workersMux.RUnlock()
	s.tasksMux.Unlock()

	assigned := 0
	for i, task := range pendingTasks {
		if i >= len(idleWorkers) {
			break // not enough
		}
		worker := idleWorkers[i]
		if s.assignTaskToWorker(task, worker) {
			assigned++
		}
	}
}

// 整理需要重新分配的task，释放这些tasker
func (s *Server) startTaskMonitor() {
	ticker := time.NewTicker(5 * time.Second) // 每5秒检查一次
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.monitorTasks()
		case <-s.stopChan:
			return
		}
	}
}

func (s *Server) monitorTasks() {
	s.tasksMux.Lock()
	defer s.tasksMux.Unlock()

	now := time.Now()
	pendingTasks := make([]*TaskInfo, 0)
	DoneTaskNum := 0
	for _, task := range s.tasks {
		if task.Status == TaskStatusDoing {
			if now.Sub(task.UpdatedAt) > s.taskTimeout {
				log.Printf("任务 %s 执行超时，标记为pending", task.ID)
				task.Status = TaskStatusPending
				pendingTasks = append(pendingTasks, task)
			}
		} else if task.Status == TaskStatusPending {
			pendingTasks = append(pendingTasks, task)
		}
	}
	if DoneTaskNum == len(s.tasks) {
		log.WithFields(logrus.Fields{
			"number": len(pendingTasks),
		}).Info("Finish all tasks")
	}
	log.WithFields(logrus.Fields{
		"number": len(pendingTasks),
	}).Info("monitor pending tasks")
	// 重新分配risking任务
	if len(pendingTasks) > 0 {
		defer s.triggerSchedule()
	}
	for _, task := range pendingTasks {
		s.clearAndPendingTask(task)
	}
}

// 分配任务给worker
func (s *Server) assignTaskToWorker(task *TaskInfo, worker *Worker) bool {
	// 通过gRPC调用worker
	conn, err := grpc.Dial(worker.Address, grpc.WithInsecure())
	if err != nil {
		log.Printf("连接Worker %s 失败: %v", worker.WorkerID, err)
		return false
	}
	defer conn.Close()

	client := workerpb.NewTicketWorkerClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req := &workerpb.TaskRequest{
		TaskId:      task.ID,
		TicketsInfo: task.TickerConfigContent,
	}

	reply, err := client.PushTask(ctx, req)
	if err != nil {
		log.Printf("分配任务给Worker %s 失败: %v", worker.WorkerID, err)
		return false
	}

	if !reply.Success {
		log.Printf("Worker %s 拒绝任务: %s", worker.WorkerID, reply.Message)
		return false
	}

	// 更新状态
	s.tasksMux.Lock()
	task.Status = TaskStatusDoing
	task.AssignedTo = worker.WorkerID
	task.UpdatedAt = time.Now()
	s.tasksMux.Unlock()

	s.workersMux.Lock()
	worker.Status = Working
	worker.TaskAssigned = task.ID
	s.workersMux.Unlock()
	log.Printf("任务成功分配 <%s> => <%s>", task.TaskName, worker.Address)
	return true
}

// 重新分配任务
func (s *Server) clearAndPendingTask(task *TaskInfo) {
	task.RetryCount++
	task.Status = TaskStatusPending
	task.AssignedTo = ""
	task.UpdatedAt = time.Now()
}

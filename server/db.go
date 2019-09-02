package server

import (
    "sync"
    "time"
    "sync/atomic"
    "github.com/snower/slock/protocol"
)

const TIMEOUT_QUEUE_LENGTH int64 = 0x10
const EXPRIED_QUEUE_LENGTH int64 = 0x10
const TIMEOUT_QUEUE_LENGTH_MASK int64 = 0x0f
const EXPRIED_QUEUE_LENGTH_MASK int64 = 0x0f
const TIMEOUT_QUEUE_MAX_WAIT int64 = 0x08
const EXPRIED_QUEUE_MAX_WAIT int64 = 0x08

type LockDB struct {
    slock                       *SLock
    locks                       map[[2]uint64]*LockManager
    timeout_locks               [][]*LockQueue
    expried_locks               [][]*LockQueue
    current_time                int64
    check_timeout_time          int64
    check_expried_time          int64
    glock                       sync.Mutex
    manager_glocks              []*sync.Mutex
    free_lock_managers          []*LockManager
    free_locks                  []*LockQueue
    free_lock_manager_count     int32
    manager_glock_index         int8
    manager_max_glocks          int8
    is_stop                     bool
    state                       protocol.LockDBState
}

func NewLockDB(slock *SLock) *LockDB {
    manager_max_glocks := int8(64)
    manager_glocks := make([]*sync.Mutex, manager_max_glocks)
    free_locks := make([]*LockQueue, manager_max_glocks)
    for i:=int8(0); i< manager_max_glocks; i++{
        manager_glocks[i] = &sync.Mutex{}
        free_locks[i] = NewLockQueue(2, 16, 4096)
    }

    now := time.Now().Unix()
    db := &LockDB{slock, make(map[[2]uint64]*LockManager, 4194304), make([][]*LockQueue, TIMEOUT_QUEUE_LENGTH),
        make([][]*LockQueue, EXPRIED_QUEUE_LENGTH), now, now, now, sync.Mutex{},
        manager_glocks, make([]*LockManager, 4194304), free_locks, -1,
        0, manager_max_glocks, false, protocol.LockDBState{}}

    db.ResizeTimeOut()
    db.ResizeExpried()
    go db.UpdateCurrentTime()
    go db.CheckTimeOut()
    go db.CheckExpried()
    return db
}

func (self *LockDB) ConvertUint642ToByte16(uint642 [2]uint64) [16]byte {
    return [16]byte{byte(uint642[0]), byte(uint642[0] >> 8), byte(uint642[0] >> 16), byte(uint642[0] >> 24),
        byte(uint642[0] >> 32), byte(uint642[0] >> 40), byte(uint642[0] >> 48), byte(uint642[0] >> 56),
        byte(uint642[1]), byte(uint642[1] >> 8), byte(uint642[1] >> 16), byte(uint642[1] >> 24),
        byte(uint642[1] >> 32), byte(uint642[1] >> 40), byte(uint642[1] >> 48), byte(uint642[1] >> 56)}
}

func (self *LockDB) ResizeTimeOut (){
    for i := int64(0); i < TIMEOUT_QUEUE_LENGTH; i++ {
        self.timeout_locks[i] = make([]*LockQueue, self.manager_max_glocks)
        for j := int8(0); j < self.manager_max_glocks; j++ {
            self.timeout_locks[i][j] = NewLockQueue(4, 16, 4096)
        }
    }
}

func (self *LockDB) ResizeExpried (){
    for i := int64(0); i < EXPRIED_QUEUE_LENGTH; i++ {
        self.expried_locks[i] = make([]*LockQueue, self.manager_max_glocks)
        for j := int8(0); j < self.manager_max_glocks; j++ {
            self.expried_locks[i][j] = NewLockQueue(4, 16, 4096)
        }
    }
}

func (self *LockDB) UpdateCurrentTime(){
    for !self.is_stop {
        time.Sleep(5e8)
        self.current_time = time.Now().Unix()
    }
}

func (self *LockDB) CheckTimeOut(){
    for !self.is_stop {
        time.Sleep(1e9)


        check_timeout_time := self.check_timeout_time

        now := self.current_time
        self.check_timeout_time = now + 1
        for _, manager_glock := range self.manager_glocks {
            manager_glock.Lock()
            manager_glock.Unlock()
        }

        for ; check_timeout_time <= now; {
            go self.CheckTimeTimeOut(check_timeout_time, now)
            check_timeout_time++
        }
    }
}

func (self *LockDB) CheckTimeTimeOut(check_timeout_time int64, now int64) {
    timeout_locks := self.timeout_locks[check_timeout_time & TIMEOUT_QUEUE_LENGTH_MASK]
    do_timeout_locks := make([]*Lock, 0)

    for i := int8(0); i < self.manager_max_glocks; i++ {
        self.manager_glocks[i].Lock()

        lock := timeout_locks[i].Pop()
        for ; lock != nil; {
            if !lock.timeouted {
                if lock.timeout_time > now {
                    lock.timeout_checked_count++
                    self.AddTimeOut(lock)
                    lock = timeout_locks[i].Pop()
                    continue
                }

                do_timeout_locks = append(do_timeout_locks, lock)
                lock = timeout_locks[i].Pop()
                continue
            }

            lock_manager := lock.manager
            lock.ref_count--
            if lock.ref_count == 0 {
                lock_manager.FreeLock(lock)
            }

            if lock_manager.ref_count == 0 {
                self.RemoveLockManager(lock_manager)
            }

            lock = timeout_locks[i].Pop()
        }

        timeout_locks[i].Reset()
        self.manager_glocks[i].Unlock()
    }

    for _, lock := range do_timeout_locks {
        self.DoTimeOut(lock)
    }
}

func (self *LockDB) CheckExpried(){
    for !self.is_stop {
        time.Sleep(1e9)

        check_expried_time := self.check_expried_time

        now := self.current_time
        self.check_expried_time = now + 1
        for _, manager_glock := range self.manager_glocks {
            manager_glock.Lock()
            manager_glock.Unlock()
        }

        for ; check_expried_time <= now; {
            go self.CheckTimeExpried(check_expried_time, now)
            check_expried_time++
        }

    }
}

func (self *LockDB) CheckTimeExpried(check_expried_time int64, now int64){
    expried_locks := self.expried_locks[check_expried_time & EXPRIED_QUEUE_LENGTH_MASK]
    do_expried_locks := make([]*Lock, 0)

    for i := int8(0); i < self.manager_max_glocks; i++ {
        self.manager_glocks[i].Lock()

        lock := expried_locks[i].Pop()
        for ; lock != nil; {
            if !lock.expried {
                if lock.expried_time > now {
                    lock.expried_checked_count++
                    self.AddExpried(lock)

                    lock = expried_locks[i].Pop()
                    continue
                }

                do_expried_locks = append(do_expried_locks, lock)
                lock = expried_locks[i].Pop()
                continue
            }

            lock_manager := lock.manager
            lock.ref_count--
            if lock.ref_count == 0 {
                lock_manager.FreeLock(lock)
            }

            if lock_manager.ref_count == 0 {
                self.RemoveLockManager(lock_manager)
            }
            lock = expried_locks[i].Pop()
        }

        expried_locks[i].Reset()
        self.manager_glocks[i].Unlock()
    }

    for _, lock := range do_expried_locks {
        self.DoExpried(lock)
    }
}


func (self *LockDB) GetOrNewLockManager(command *protocol.LockCommand) *LockManager{
    self.glock.Lock()

    lock_manager, ok := self.locks[command.LockKey]
    if ok {
        self.glock.Unlock()
        return lock_manager
    }

    if self.free_lock_manager_count < 0 {
        lock_managers := make([]LockManager, 4096)

        for i := 0; i < 4096; i++ {
            lock_managers[i].lock_db = self
            lock_managers[i].db_id = command.DbId
            lock_managers[i].locks = NewLockQueue(4, 16, 4)
            lock_managers[i].lock_maps = make(map[[2]uint64]*Lock, 8)
            lock_managers[i].wait_locks = NewLockQueue(4, 16, 4)
            lock_managers[i].glock = self.manager_glocks[self.manager_glock_index]
            lock_managers[i].glock_index = self.manager_glock_index
            lock_managers[i].free_locks = self.free_locks[self.manager_glock_index]

            self.manager_glock_index++
            if self.manager_glock_index >= self.manager_max_glocks {
                self.manager_glock_index = 0
            }
            self.free_lock_manager_count++
            self.free_lock_managers[self.free_lock_manager_count] = &lock_managers[i]
        }
    }

    lock_manager = self.free_lock_managers[self.free_lock_manager_count]
    self.free_lock_manager_count--
    lock_manager.freed = false
    self.locks[command.LockKey] = lock_manager
    self.state.KeyCount++
    self.glock.Unlock()

    lock_manager.lock_key = command.LockKey
    return lock_manager
}

func (self *LockDB) GetLockManager(command *protocol.LockCommand) *LockManager{
    self.glock.Lock()

    lock_manager, ok := self.locks[command.LockKey]
    if ok {
        self.glock.Unlock()
        return lock_manager
    }

    self.glock.Unlock()
    return nil
}

func (self *LockDB) RemoveLockManager(lock_manager *LockManager){
    self.glock.Lock()
    if !lock_manager.freed {
        delete(self.locks, lock_manager.lock_key)
        lock_manager.freed = true

        if self.free_lock_manager_count < 4194303 {
            self.free_lock_manager_count++
            self.free_lock_managers[self.free_lock_manager_count] = lock_manager
            self.state.KeyCount--

            if lock_manager.locks != nil {
                lock_manager.locks.Reset()
            }
            if lock_manager.wait_locks != nil {
                lock_manager.wait_locks.Reset()
            }
            self.glock.Unlock()
        } else {
            self.state.KeyCount--
            self.glock.Unlock()

            lock_manager.current_lock = nil
            lock_manager.locks = nil
            lock_manager.lock_maps = nil
            lock_manager.wait_locks = nil
            lock_manager.free_locks = nil
        }
        return
    }

    self.glock.Unlock()
}

func (self *LockDB) AddTimeOut(lock *Lock){
    lock.timeouted = false

    if lock.timeout_checked_count > TIMEOUT_QUEUE_MAX_WAIT {
        timeout_time := self.check_timeout_time + TIMEOUT_QUEUE_MAX_WAIT
        if lock.timeout_time < timeout_time {
            timeout_time = lock.timeout_time
            if timeout_time < self.check_timeout_time {
                timeout_time = self.check_timeout_time
            }
        }

        self.timeout_locks[timeout_time & TIMEOUT_QUEUE_LENGTH_MASK][lock.manager.glock_index].Push(lock)
    } else {
        timeout_time := self.check_timeout_time + lock.timeout_checked_count
        if lock.timeout_time < timeout_time {
            timeout_time = lock.timeout_time
            if timeout_time < self.check_timeout_time {
                timeout_time = self.check_timeout_time
            }
        }

        self.timeout_locks[timeout_time & TIMEOUT_QUEUE_LENGTH_MASK][lock.manager.glock_index].Push(lock)
    }
}

func (self *LockDB) RemoveTimeOut(lock *Lock){
    lock.timeouted = true
    atomic.AddUint32(&self.state.WaitCount, 0xffffffff)
}

func (self *LockDB) DoTimeOut(lock *Lock){
    lock_manager := lock.manager
    lock_manager.glock.Lock()
    if lock.timeouted {
        lock.ref_count--
        if lock.ref_count == 0 {
            lock_manager.FreeLock(lock)
        }

        if lock_manager.ref_count == 0 {
            self.RemoveLockManager(lock_manager)
        }
        lock_manager.glock.Unlock()
        return
    }

    lock.timeouted = true
    lock_protocol, lock_command := lock.protocol, lock.command
    lock_manager.GetWaitLock()
    lock.ref_count--
    if lock.ref_count == 0 {
        lock_manager.FreeLock(lock)
    }

    if lock_manager.ref_count == 0 {
        self.RemoveLockManager(lock_manager)
    }
    lock_manager.glock.Unlock()

    command_expried := lock_command.Expried
    self.slock.Active(lock_protocol, lock_command, protocol.RESULT_TIMEOUT, lock_manager.locked, false)
    self.slock.FreeLockCommand(lock_command)
    atomic.AddUint32(&self.state.WaitCount, 0xffffffff)
    atomic.AddUint32(&self.state.TimeoutedCount, 1)

    if command_expried > 0 {
        self.slock.Log().Infof("LockTimeout DbId:%d LockKey:%x LockId:%x RequestId:%x RemoteAddr:%s", lock_command.DbId,
            self.ConvertUint642ToByte16(lock_command.LockKey), self.ConvertUint642ToByte16(lock_command.LockId),
            self.ConvertUint642ToByte16(lock_command.RequestId), lock_protocol.RemoteAddr().String())
    }
}

func (self *LockDB) AddExpried(lock *Lock){
    lock.expried = false

    if lock.expried_checked_count > EXPRIED_QUEUE_MAX_WAIT {
        expried_time := self.check_expried_time + EXPRIED_QUEUE_MAX_WAIT
        if lock.expried_time < expried_time {
            expried_time = lock.expried_time
            if expried_time < self.check_expried_time {
                expried_time = self.check_expried_time
            }
        }

        self.expried_locks[expried_time & EXPRIED_QUEUE_LENGTH_MASK][lock.manager.glock_index].Push(lock)
    } else {
        expried_time := self.check_expried_time + lock.expried_checked_count
        if lock.expried_time < expried_time {
            expried_time = lock.expried_time
            if expried_time < self.check_expried_time {
                expried_time = self.check_expried_time
            }
        }

        self.expried_locks[expried_time & EXPRIED_QUEUE_LENGTH_MASK][lock.manager.glock_index].Push(lock)
    }
}

func (self *LockDB) RemoveExpried(lock *Lock){
    lock.expried = true
}

func (self *LockDB) DoExpried(lock *Lock){
    lock_manager := lock.manager
    lock_manager.glock.Lock()
    if lock.expried {
        lock.ref_count--
        if lock.ref_count == 0 {
            lock_manager.FreeLock(lock)
        }

        if lock_manager.ref_count == 0 {
            self.RemoveLockManager(lock_manager)
        }
        lock_manager.glock.Unlock()
        return
    }

    lock_locked := lock.locked
    lock.expried = true
    lock_manager.locked-=uint16(lock_locked)
    lock_protocol, lock_command := lock.protocol, lock.command
    lock_manager.RemoveLock(lock)

    wait_lock := lock_manager.GetWaitLock()
    lock.ref_count--
    if lock.ref_count == 0 {
        lock_manager.FreeLock(lock)
    }

    if lock_manager.ref_count == 0 {
        self.RemoveLockManager(lock_manager)
    }
    lock_manager.glock.Unlock()

    self.slock.Active(lock_protocol, lock_command, protocol.RESULT_EXPRIED, lock_manager.locked, false)
    self.slock.FreeLockCommand(lock_command)
    atomic.AddUint32(&self.state.LockedCount, 0xffffffff - uint32(lock_locked) + 1)
    atomic.AddUint32(&self.state.ExpriedCount, uint32(lock_locked))

    self.slock.Log().Infof("LockExpried DbId:%d LockKey:%x LockId:%x RequestId:%x RemoteAddr:%s", lock_command.DbId,
        self.ConvertUint642ToByte16(lock_command.LockKey), self.ConvertUint642ToByte16(lock_command.LockId),
        self.ConvertUint642ToByte16(lock_command.RequestId), lock_protocol.RemoteAddr().String())

    if wait_lock != nil {
        lock_manager.glock.Lock()
        for ;; {
            if wait_lock.manager == nil {
                if !lock_manager.freed {
                    wait_lock = lock_manager.GetWaitLock()
                    if wait_lock == nil {
                        lock_manager.waited = false
                        return
                    }
                    continue
                }
            }

            if !self.DoLock(lock_manager, wait_lock) {
                lock_manager.glock.Unlock()
                return
            }

            wait_lock = self.WakeUpWaitLock(lock_manager, wait_lock, nil)
            if wait_lock != nil {
                lock_manager.glock.Lock()
                continue
            }
            lock_manager.waited = false
            return
        }
    }
}

func (self *LockDB) Lock(server_protocol *ServerProtocol, command *protocol.LockCommand) (err error) {
    lock_manager := self.GetOrNewLockManager(command)
    lock_manager.glock.Lock()

    if lock_manager.freed {
        lock_manager.glock.Unlock()
        return self.Lock(server_protocol, command)
    }

    if lock_manager.locked > 0 {
        if command.Flag == 0x01 {
            lock_manager.glock.Unlock()

            current_lock := lock_manager.current_lock
            command.LockId = current_lock.command.LockId
            command.Expried = uint32(current_lock.expried_time - current_lock.start_time)
            command.Timeout = current_lock.command.Timeout
            command.Count = current_lock.command.Count
            command.Rcount = current_lock.command.Rcount

            self.slock.Active(server_protocol, command, protocol.RESULT_UNOWN_ERROR, lock_manager.locked, true)
            server_protocol.FreeLockCommand(command)
            return nil
        }

        current_lock := lock_manager.GetLockedLock(command)
        if current_lock != nil {
            if command.Flag == 0x02 {
                lock_manager.UpdateLockedLock(current_lock, command.Timeout, command.Expried, command.Count, command.Rcount)
                lock_manager.glock.Unlock()

                command.Expried = uint32(current_lock.expried_time - current_lock.start_time)
                command.Timeout = current_lock.command.Timeout
                command.Count = current_lock.command.Count
                command.Rcount = current_lock.command.Rcount
            } else if(current_lock.locked <= command.Rcount){
                if(command.Expried == 0) {
                    lock_manager.glock.Unlock()

                    command.Expried = uint32(current_lock.expried_time - current_lock.start_time)
                    command.Timeout = current_lock.command.Timeout
                    command.Count = current_lock.command.Count
                    command.Rcount = current_lock.command.Rcount

                    self.slock.Active(server_protocol, command, protocol.RESULT_LOCKED_ERROR, uint16(current_lock.locked), true)
                    server_protocol.FreeLockCommand(command)
                    return nil
                }

                lock_manager.locked++
                current_lock.locked++
                lock_manager.UpdateLockedLock(current_lock, command.Timeout, command.Expried, command.Count, command.Rcount)
                lock_manager.glock.Unlock()

                self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
                server_protocol.FreeLockCommand(command)
                atomic.AddUint64(&self.state.LockCount, 1)
                atomic.AddUint32(&self.state.LockedCount, 1)
                return nil
            } else {
                lock_manager.glock.Unlock()
            }

            self.slock.Active(server_protocol, command, protocol.RESULT_LOCKED_ERROR, lock_manager.locked, true)
            server_protocol.FreeLockCommand(command)
            return nil
        }
    }

    lock := lock_manager.GetOrNewLock(server_protocol, command)
    if self.DoLock(lock_manager, lock) {
        if command.Expried > 0 {
            lock_manager.AddLock(lock)
            lock_manager.locked++
            self.AddExpried(lock)
            lock.ref_count++
            lock_manager.glock.Unlock()

            self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
            atomic.AddUint64(&self.state.LockCount, 1)
            atomic.AddUint32(&self.state.LockedCount, 1)
            return nil
        }

        lock_manager.FreeLock(lock)
        if lock_manager.ref_count == 0 {
            self.RemoveLockManager(lock_manager)
        }
        lock_manager.glock.Unlock()

        self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
        server_protocol.FreeLockCommand(command)
        atomic.AddUint64(&self.state.LockCount, 1)
        return nil
    }

    if command.Timeout > 0 {
        lock_manager.AddWaitLock(lock)
        self.AddTimeOut(lock)
        lock.ref_count++
        lock_manager.glock.Unlock()

        atomic.AddUint32(&self.state.WaitCount, 1)
        return nil
    }

    lock_manager.FreeLock(lock)
    if lock_manager.ref_count == 0 {
        self.RemoveLockManager(lock_manager)
    }
    lock_manager.glock.Unlock()

    self.slock.Active(server_protocol, command, protocol.RESULT_TIMEOUT, lock_manager.locked, true)
    server_protocol.FreeLockCommand(command)
    return nil
}

func (self *LockDB) UnLock(server_protocol *ServerProtocol, command *protocol.LockCommand) (err error) {
    lock_manager := self.GetLockManager(command)
    if lock_manager == nil {
        self.slock.Active(server_protocol, command, protocol.RESULT_UNLOCK_ERROR, 0, true)
        server_protocol.FreeLockCommand(command)
        atomic.AddUint32(&self.state.UnlockErrorCount, 1)
        return nil
    }

    lock_manager.glock.Lock()

    if lock_manager.locked == 0 {
        lock_manager.glock.Unlock()

        self.slock.Active(server_protocol, command, protocol.RESULT_UNLOCK_ERROR, lock_manager.locked, true)
        server_protocol.FreeLockCommand(command)
        atomic.AddUint32(&self.state.UnlockErrorCount, 1)
        return nil
    }

    current_lock := lock_manager.GetLockedLock(command)
    if current_lock == nil {
        current_lock = lock_manager.current_lock

        if command.Flag == 0x01 {
            if current_lock == nil {
                lock_manager.glock.Unlock()

                self.slock.Active(server_protocol, command, protocol.RESULT_UNOWN_ERROR, lock_manager.locked, true)
                server_protocol.FreeLockCommand(command)
                atomic.AddUint32(&self.state.UnlockErrorCount, 1)
                return nil
            }

            command.LockId = current_lock.command.LockId
        } else {
            lock_manager.glock.Unlock()

            self.slock.Active(server_protocol, command, protocol.RESULT_UNOWN_ERROR, lock_manager.locked, true)
            server_protocol.FreeLockCommand(command)
            atomic.AddUint32(&self.state.UnlockErrorCount, 1)
            return nil
        }
    }

    wait_lock := lock_manager.GetWaitLock()
    if current_lock.locked > 1 {
        if command.Rcount == 0 {
            //self.RemoveExpried(current_lock)
            lock_locked := current_lock.locked
            current_lock.expried = true
            current_lock_command := current_lock.command
            lock_manager.RemoveLock(current_lock)
            lock_manager.locked-=uint16(lock_locked)
            lock_manager.glock.Unlock()

            self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
            server_protocol.FreeLockCommand(command)
            server_protocol.FreeLockCommand(current_lock_command)

            atomic.AddUint64(&self.state.UnLockCount, uint64(lock_locked))
            atomic.AddUint32(&self.state.LockedCount, 0xffffffff - uint32(lock_locked) + 1)
        } else {
            lock_manager.locked--
            current_lock.locked--
            lock_manager.glock.Unlock()

            self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
            server_protocol.FreeLockCommand(command)

            atomic.AddUint64(&self.state.UnLockCount, 1)
            atomic.AddUint32(&self.state.LockedCount, 0xffffffff)
        }
    } else {
        //self.RemoveExpried(current_lock)
        current_lock.expried = true
        current_lock_command := current_lock.command
        lock_manager.RemoveLock(current_lock)
        lock_manager.locked--
        lock_manager.glock.Unlock()

        self.slock.Active(server_protocol, command, protocol.RESULT_SUCCED, lock_manager.locked, true)
        server_protocol.FreeLockCommand(command)
        server_protocol.FreeLockCommand(current_lock_command)

        atomic.AddUint64(&self.state.UnLockCount, 1)
        atomic.AddUint32(&self.state.LockedCount, 0xffffffff)
    }

    if wait_lock != nil {
        lock_manager.glock.Lock()
        for ;; {
            if wait_lock.manager == nil {
                if !lock_manager.freed {
                    wait_lock = lock_manager.GetWaitLock()
                    if wait_lock == nil {
                        lock_manager.waited = false
                        return
                    }
                    continue
                }
            }

            if !self.DoLock(lock_manager, wait_lock) {
                lock_manager.glock.Unlock()
                return nil
            }

            wait_lock = self.WakeUpWaitLock(lock_manager, wait_lock, server_protocol)
            if wait_lock != nil {
                lock_manager.glock.Lock()
                continue
            }
            lock_manager.waited = false
            return nil
        }
    }
    return nil
}

func (self *LockDB) DoLock(lock_manager *LockManager, lock *Lock) bool{
    if lock_manager.locked == 0 {
        return true
    }

    if lock_manager.waited {
        return false
    }

    if(lock_manager.locked <= lock_manager.current_lock.command.Count){
        if(lock_manager.locked <= lock.command.Count) {
            return true
        }
    }

    return false
}

func (self *LockDB) WakeUpWaitLock(lock_manager *LockManager, wait_lock *Lock, server_protocol *ServerProtocol) *Lock {
    if wait_lock.timeouted {
        wait_lock = lock_manager.GetWaitLock()
        lock_manager.glock.Unlock()
        return wait_lock
    }

    //self.RemoveTimeOut(wait_lock)
    wait_lock.timeouted = true

    if wait_lock.command.Expried > 0 {
        lock_manager.AddLock(wait_lock)
        lock_manager.locked++
        self.AddExpried(wait_lock)
        wait_lock.ref_count++
        lock_manager.GetWaitLock()
        lock_manager.glock.Unlock()

        self.slock.Active(wait_lock.protocol, wait_lock.command, protocol.RESULT_SUCCED, lock_manager.locked, wait_lock.protocol == server_protocol)
        atomic.AddUint64(&self.state.LockCount, 1)
        atomic.AddUint32(&self.state.LockedCount, 1)
        atomic.AddUint32(&self.state.WaitCount, 0xffffffff)
        return nil
    }

    wait_lock_protocol, wait_lock_command := wait_lock.protocol, wait_lock.command
    wait_lock = lock_manager.GetWaitLock()
    if lock_manager.ref_count == 0 {
        self.RemoveLockManager(lock_manager)
    }
    lock_manager.glock.Unlock()

    if wait_lock_protocol == server_protocol {
        self.slock.Active(wait_lock_protocol, wait_lock_command, protocol.RESULT_SUCCED, lock_manager.locked, true)
        server_protocol.FreeLockCommand(wait_lock_command)
    } else {
        self.slock.Active(wait_lock_protocol, wait_lock_command, protocol.RESULT_SUCCED, lock_manager.locked, false)
        self.slock.FreeLockCommand(wait_lock_command)
    }

    atomic.AddUint64(&self.state.LockCount, 1)
    atomic.AddUint32(&self.state.WaitCount, 0xffffffff)
    return wait_lock
}

func (self *LockDB) GetState() *protocol.LockDBState {
    return &self.state
}
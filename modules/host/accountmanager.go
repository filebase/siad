package host

import (
	"path/filepath"
	"sync"
	"time"

	"gitlab.com/NebulousLabs/Sia/build"
	"gitlab.com/NebulousLabs/Sia/persist"
	"gitlab.com/NebulousLabs/Sia/types"
	"gitlab.com/NebulousLabs/errors"
)

const (
	accountExpiryTimeout = 7 * 86400
)

var (
	// amPersistFilename defines the name of the file that holds the account
	// manager's persistence
	amPersistFilename = "accountmanager.json"

	// amPersistMetadata is the header that is used when writing the account
	// manager's state to disk.
	amPersistMetadata = persist.Metadata{
		Header:  "Account Manager Persistence",
		Version: "1.4.1.3",
	}

	// accountMaxBalance is the maximum balance an ephemeral account is allowed
	// to hold
	accountMaxBalance = types.NewCurrency64(10 ^ 28)

	// blockedCallTimeout is the maximum amount of time we wait for an account
	// to have a certain balance
	blockedCallTimeout = build.Select(build.Var{
		Standard: 15 * time.Minute,
		Dev:      15 * time.Second,
		Testing:  3 * time.Second,
	}).(time.Duration)

	// pruneExpiredAccountsFrequency is the frequency at which the hosts prunes
	// accounts which have been stale
	pruneExpiredAccountsFrequency = build.Select(build.Var{
		Standard: 1 * time.Hour,
		Dev:      15 * time.Second,
		Testing:  3 * time.Second,
	}).(time.Duration)
)

type (
	// accountManager is a subsystem responsible for managing ephemeral
	// accounts.
	//
	// These accounts are a pubkey with a balance associated to it. They are
	// kept completely off-chain and serve as a method of payment.
	//
	// The account owner fully entrusts the money with the host, he has no
	// recourse at all if the host decides to steal the funds. The total amount
	// an account can hold is capped and the amounts that get deposited and
	// spent are very small to mitigate this.
	//
	// All operations on the account have ACID properties.
	accountManager struct {
		accounts map[string]types.Currency
		receipts map[string]string
		updated  map[string]int64
		deposits map[string]chan bool

		mu sync.Mutex

		persistSig chan bool
		persistDir string

		// Keep track of total expired funds
		totalExpired types.Currency

		*hostUtils
	}

	// amPersist contains all account manager data we want to persist
	amPersist struct {
		Accounts     map[string]types.Currency
		TotalExpired types.Currency
	}
)

// newAccountManager returns a new account manager ready for use by the host
func (h *Host) newAccountManager(persistDir string) *accountManager {
	am := &accountManager{
		accounts: make(map[string]types.Currency),
		receipts: make(map[string]string),
		updated:  make(map[string]int64),
		deposits: make(map[string]chan bool),

		persistSig: make(chan bool),
		persistDir: persistDir,

		totalExpired: types.ZeroCurrency,

		hostUtils: h.hostUtils,
	}

	err := am.load()
	if err != nil {
		am.log.Severe("Unable to load account manager state:", err)
	}

	go am.threadedPruneExpiredAccounts()

	am.tg.OnStop(func() {
		close(am.persistSig)
		for _, d := range am.deposits {
			close(d)
		}
	})

	return am
}

// managedDeposit will credit the amount to the account's balance
func (am *accountManager) managedDeposit(id string, amount types.Currency) error {
	err := am.tg.Add()
	if err != nil {
		return err
	}
	defer am.tg.Done()

	am.mu.Lock()
	defer am.mu.Unlock()

	balance, exists := am.accounts[id]
	if !exists {
		balance = types.ZeroCurrency
	}

	// Verify the updated balance does not exceed the max account balance
	uBalance := balance.Add(amount)
	if accountMaxBalance.Cmp(uBalance) < 0 {
		am.hostUtils.log.Printf("ERROR: deposit of %v exceeded max balance for account %v", amount, id)
		return errors.New("deposit exceeds max account balance")
	}
	am.accounts[id] = uBalance
	am.updated[id] = time.Now().Unix()

	// Notify blocking threads of this deposit, we send the balance through the
	// channel to avoid having to acquire a lock to check if its sufficient
	_, exists = am.deposits[id]
	if !exists {
		am.deposits[id] = make(chan bool)
	}
	am.deposits[id] <- true

	// Trigger a persist
	am.persistSig <- true

	return nil
}

func (am *accountManager) managedSpend(id string, amount types.Currency, receipt string) error {
	err := am.tg.Add()
	if err != nil {
		return err
	}
	defer am.tg.Done()

	am.mu.Lock()

	// Verify account
	_, exists := am.accounts[id]
	if !exists {
		am.mu.Unlock()
		return errors.New("ERROR: unknown account")
	}

	// Verify receipt
	_, exists = am.receipts[receipt]
	if exists {
		am.hostUtils.log.Printf("ERROR: receipt %v was already spent", receipt)
		am.mu.Unlock()
		return errors.New("spend was already executed")
	}

	// Ensure deposit sig channel
	_, exists = am.deposits[id]
	if !exists {
		am.deposits[id] = make(chan bool)
	}

	// If current account balance is insufficient, we block until either the
	// blockCallTimeout expires, the account receives sufficient deposits or we
	// receive a message on the thread group's stop channel
	if am.accounts[id].Cmp(amount) < 0 {
		am.mu.Unlock()
		for {
			select {
			case <-am.tg.StopChan():
				return errors.New("ERROR: spend cancelled, stop received")
			case <-am.deposits[id]:
				am.mu.Lock()
				if am.accounts[id].Cmp(amount) < 0 {
					am.mu.Unlock()
					continue
				}
				break
			case <-time.After(blockedCallTimeout):
				return errors.New("ERROR: spend timeout, insufficient balance")
			}
		}
	}

	am.accounts[id] = am.accounts[id].Sub(amount)
	am.updated[id] = time.Now().Unix()
	am.mu.Unlock()

	return nil
}

// threadedPruneExpiredAccounts will expire accounts which have been inactive
func (am *accountManager) threadedPersistLoop() {
	err := am.tg.Add()
	if err != nil {
		return
	}
	defer am.tg.Done()

	for {
		select {
		case <-am.tg.StopChan():
			return
		case <-am.persistSig:
			am.mu.Lock()
			am.save()
			am.mu.Unlock()
			continue
		}
	}
}

// save will persist the account manager persistence object to disk
func (am *accountManager) save() error {
	data := amPersist{am.accounts, am.totalExpired}
	return am.dependencies.SaveFileSync(amPersistMetadata, data, filepath.Join(am.persistDir, amPersistFilename))
}

// load reinstates the saved persistence object from disk
func (am *accountManager) load() error {
	var data amPersist
	data.Accounts = make(map[string]types.Currency)
	data.TotalExpired = types.ZeroCurrency

	path := filepath.Join(am.persistDir, amPersistFilename)
	err := am.dependencies.LoadFile(amPersistMetadata, &data, path)
	if err != nil {
		return errors.AddContext(err, "filepath: "+path)
	}

	am.accounts = data.Accounts
	am.totalExpired = data.TotalExpired

	return nil
}

// threadedPruneExpiredAccounts will expire accounts which have been inactive
func (am *accountManager) threadedPruneExpiredAccounts() {
	err := am.tg.Add()
	if err != nil {
		return
	}
	defer am.tg.Done()

	for {
		now := time.Now().Unix()
		for id, balance := range am.accounts {
			last, exists := am.updated[id]
			if !exists || now-last > 0 {
				am.mu.Lock()
				am.totalExpired = am.totalExpired.Add(balance)
				delete(am.accounts, id)
				delete(am.deposits, id)
				am.save()
				am.mu.Unlock()
			}
		}

		// Block until next cycle.
		select {
		case <-am.tg.StopChan():
			return
		case <-time.After(pruneExpiredAccountsFrequency):
			continue
		}
	}
}

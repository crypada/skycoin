package coin

import (
    "bytes"
    "errors"
    "github.com/skycoin/encoder"
    "log"
    "math"
    "sort"
)

type Transaction struct {
    Head TransactionHeader //Outer Hash
    In   []SHA256
    Out  []TransactionOutput
}

type TransactionHeader struct { //not hashed
    Hash SHA256 //inner hash
    Sigs []Sig  //list of signatures, 64+1 bytes each
}

//hash output/name is function of Hash
type TransactionOutput struct {
    Address Address //address to send to
    Coins   uint64  //amount to be sent in coins
    Hours   uint64  //amount to be sent in coin hours
}

// Verify attempts to determine if the transaction is well formed
// Verify cannot check transaction signatures, it needs the address from unspents
// Verify cannot check if outputs being spent exist
// Verify cannot check if the transaction would create or destroy coins
// or if the inputs have the required coin base
func (self *Transaction) Verify(maxSize int) error {
    h := self.hashInner()
    if h != self.Head.Hash {
        return errors.New("Invalid header hash")
    }

    if len(self.In) == 0 {
        return errors.New("No inputs")
    }
    if len(self.Out) == 0 {
        return errors.New("No outputs")
    }

    // Check signature index fields
    if len(self.Head.Sigs) != len(self.In) {
        return errors.New("Invalid number of signatures")
    }
    if len(self.Head.Sigs) >= math.MaxUint16 {
        return errors.New("Too many signatures and inputs")
    }

    // Transaction are size limited
    if self.Size() > maxSize {
        return errors.New("Transaction too large")
    }

    // Check duplicate inputs
    uxOuts := make(map[SHA256]byte, len(self.In))
    for i, _ := range self.In {
        uxOuts[self.In[i]] = byte(1)
    }
    if len(uxOuts) != len(self.In) {
        return errors.New("Duplicate spend")
    }

    // Check for duplicate potential outputs
    outputs := make(map[SHA256]byte, len(self.Out))
    uxb := UxBody{
        SrcTransaction: self.Hash(),
    }
    for _, to := range self.Out {
        uxb.Coins = to.Coins
        uxb.Hours = to.Hours
        uxb.Address = to.Address
        outputs[uxb.Hash()] = byte(1)
    }
    if len(outputs) != len(self.Out) {
        return errors.New("Duplicate output in transaction")
    }

    // Validate signature
    for _, sig := range self.Head.Sigs {
        if err := VerifySignedHash(sig, self.Head.Hash); err != nil {
            return err
        }
    }

    // Artificial restriction to prevent spam
    // Must spend only multiples of 1e6
    for _, txo := range self.Out {
        if txo.Coins == 0 {
            return errors.New("Zero coin output")
        }
        if txo.Coins%1e6 != 0 {
            return errors.New("Transaction outputs must be multiple of 1e6 " +
                "base units")
        }
    }

    return nil
}

// Adds a UxArray to the Transaction given the hash of a UxOut.
// Returns the signature index for later signing
func (self *Transaction) PushInput(uxOut SHA256) uint16 {
    if len(self.In) >= math.MaxUint16 {
        log.Panic("Max transaction inputs reached")
    }
    self.In = append(self.In, uxOut)
    return uint16(len(self.In) - 1)
}

// Adds a TransactionOutput, sending coins & hours to an Address
func (self *Transaction) PushOutput(dst Address, coins, hours uint64) {
    to := TransactionOutput{
        Address: dst,
        Coins:   coins,
        Hours:   hours,
    }
    self.Out = append(self.Out, to)
}

// Signs a UxOut hash at its signature index
func (self *Transaction) signInput(idx uint16, sec SecKey, h SHA256) {
    sig := SignHash(h, sec)
    txInLen := len(self.In)
    if txInLen > math.MaxUint16 {
        log.Panic("In too large")
    }
    if idx >= uint16(txInLen) {
        log.Panic("Invalid In idx")
    }
    if int(idx) >= len(self.Head.Sigs) {
        extendBy := int(idx) - len(self.Head.Sigs) + 1
        self.Head.Sigs = append(self.Head.Sigs, make([]Sig, extendBy)...)
    }
    self.Head.Sigs[idx] = sig
}

// Signs all inputs in the transaction
func (self *Transaction) SignInputs(keys []SecKey) {
    if len(self.Head.Sigs) != 0 {
        log.Panic("Transaction has been signed")
    }
    if len(keys) != len(self.In) {
        log.Panic("Invalid number of keys")
    }
    self.Head.Sigs = make([]Sig, len(self.In))
    h := self.hashInner()
    for i, k := range keys {
        self.signInput(uint16(i), k, h)
    }
}

// Returns the encoded byte size of the transaction
func (self *Transaction) Size() int {
    return len(self.Serialize())
}

// Hashes an entire Transaction struct, including the TransactionHeader
func (self *Transaction) Hash() SHA256 {
    b := self.Serialize()
    return SumDoubleSHA256(b)
}

// Returns the encoded size and the hash of it (avoids duplicate encoding)
func (self *Transaction) SizeHash() (int, SHA256) {
    b := self.Serialize()
    return len(b), SumDoubleSHA256(b)
}

// Saves the txn body hash to TransactionHeader.Hash
func (self *Transaction) UpdateHeader() {
    self.Head.Hash = self.hashInner()
}

// Hashes only the Transaction Inputs & Outputs
func (self *Transaction) hashInner() SHA256 {
    // WARNING -- using encoder to calculate hash is prone to error.
    // Encoder connot be considered stable.
    b1 := encoder.Serialize(self.In)
    b2 := encoder.Serialize(self.Out)
    b3 := append(b1, b2...)
    return SumSHA256(b3)
}

func (self *Transaction) Serialize() []byte {
    return encoder.Serialize(*self)
}

func TransactionDeserialize(b []byte) Transaction {
    t := Transaction{}
    if err := encoder.DeserializeRaw(b, &t); err != nil {
        log.Panic("Failed to deserialize transaction")
    }
    return t
}

type Transactions []Transaction

func (self Transactions) Hashes() []SHA256 {
    hashes := make([]SHA256, len(self))
    for i, _ := range self {
        hashes[i] = self[i].Hash()
    }
    return hashes
}

// Returns the sum of contained Transactions' sizes.  It is not the size if
// serialized, since that would have a length prefix.
func (self Transactions) Size() int {
    size := 0
    for i, _ := range self {
        size += self[i].Size()
    }
    return size
}

// Returns the first n transactions whose total size is less than or equal to
// size.
func (self Transactions) TruncateBytesTo(size int) Transactions {
    total := 0
    for i, _ := range self {
        pending := self[i].Size()
        if total+pending > size {
            return self[:i]
        }
        total += pending
    }
    return self
}

// Allows sorting transactions by fee & hash
type SortableTransactions struct {
    Txns   Transactions
    Fees   []uint64
    Hashes []SHA256
}

// Given a transaction, return its fee or an error if the fee cannot be
// calculated
type FeeCalculator func(*Transaction) (uint64, error)

// Returns transactions sorted by fee per kB, and sorted by lowest hash if
// tied.  Transactions that fail in fee computation are excluded.
func SortTransactions(txns Transactions,
    feeCalc FeeCalculator) Transactions {
    sorted := newSortableTransactions(txns, feeCalc)
    sorted.Sort()
    return sorted.Txns
}

// Returns an array of txns that can be sorted by fee.  On creation, fees are
// calculated, and if any txns have invalid fee, there are removed from
// consideration
func newSortableTransactions(txns Transactions,
    feeCalc FeeCalculator) SortableTransactions {
    newTxns := make(Transactions, len(txns))
    fees := make([]uint64, len(txns))
    hashes := make([]SHA256, len(txns))
    j := 0
    for i, _ := range txns {
        fee, err := feeCalc(&txns[i])
        if err == nil {
            newTxns[j] = txns[i]
            size := 0
            size, hashes[j] = txns[i].SizeHash()
            // Calculate fee priority based on fee per kb
            fees[j] = (fee * 1024) / uint64(size)
            j++
        }
    }
    return SortableTransactions{
        Txns:   newTxns[:j],
        Fees:   fees[:j],
        Hashes: hashes[:j],
    }
}

// Sorts by tx fee, and then by hash if fee equal
func (self SortableTransactions) Sort() {
    sort.Sort(self)
}

func (self SortableTransactions) IsSorted() bool {
    return sort.IsSorted(self)
}

func (self SortableTransactions) Len() int {
    return len(self.Txns)
}

// Default sorting is fees descending, hash ascending if fees equal
func (self SortableTransactions) Less(i, j int) bool {
    if self.Fees[i] == self.Fees[j] {
        // If fees match, hashes are sorted ascending
        return bytes.Compare(self.Hashes[i][:], self.Hashes[j][:]) < 0
    }
    // Fees are sorted descending
    return self.Fees[i] > self.Fees[j]
}

func (self SortableTransactions) Swap(i, j int) {
    self.Txns[i], self.Txns[j] = self.Txns[j], self.Txns[i]
    self.Fees[i], self.Fees[j] = self.Fees[j], self.Fees[i]
    self.Hashes[i], self.Hashes[j] = self.Hashes[j], self.Hashes[i]
}

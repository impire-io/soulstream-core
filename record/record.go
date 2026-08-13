package record

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/impire-io/soulstream-core/identity"
)

// Version is the only supported wire-format version.
const Version = 1

// Wire header names. Fixed protocol tokens are capitalised exactly as shown; NATS
// headers are case-sensitive.
const (
	HeaderMsgID   = "Nats-Msg-Id"
	HeaderVersion = "Soulstream-Version"
	HeaderAuthor  = "Soulstream-Author"
	HeaderParents = "Soulstream-Parents"
	HeaderType    = "Soulstream-Type"
	HeaderTs      = "Soulstream-Ts"
	HeaderSig     = "Soulstream-Sig"

	headerPrefix = "Soulstream-"
)

// Record is one Soulstream operation. Immutable after construction; its fields are
// checked by [Record.Validate].
type Record struct {
	ID        string            // UUIDv4 (also the Nats-Msg-Id)
	Author    string            // persona slug this op is attributed to
	Parents   []string          // ordered parent op-ids; nil/empty means no parents
	Type      string            // e.g. "turn.post"; non-empty, not enumerated here
	Timestamp time.Time         // author-claimed, informational only
	Signature string            // optional; carried through, never produced/verified here
	Payload   []byte            // pure data (text/references); opaque bytes
	Extras    map[string]string // unknown Soulstream-* headers, preserved verbatim
}

// knownHeader reports whether k is a header this version interprets (so it is not an
// "extra"). Nats-Msg-Id is handled separately and is not Soulstream-prefixed.
func knownHeader(k string) bool {
	switch k {
	case HeaderVersion, HeaderAuthor, HeaderParents, HeaderType, HeaderTs, HeaderSig:
		return true
	default:
		return false
	}
}

// Validate checks the record's fields without serialising.
func (r Record) Validate() error {
	if r.ID == "" {
		return missingField(HeaderMsgID)
	}
	if _, err := uuid.Parse(r.ID); err != nil {
		return badID(HeaderMsgID, "is not a valid uuid")
	}
	if r.Author == "" {
		return missingField(HeaderAuthor)
	}
	if err := identity.CheckName(r.Author); err != nil {
		return badAuthor(HeaderAuthor, err.Error())
	}
	if r.Type == "" {
		return missingField(HeaderType)
	}
	if r.Timestamp.IsZero() {
		return missingField(HeaderTs)
	}
	for _, p := range r.Parents {
		if _, err := uuid.Parse(p); err != nil {
			return badID(HeaderParents, "parent is not a valid uuid: "+p)
		}
	}
	return nil
}

// Build validates the record and returns its wire form: the header set (including
// Nats-Msg-Id == ID and Soulstream-Version) and the payload bytes. An empty Parents
// set produces no Soulstream-Parents header; an empty Signature produces no
// Soulstream-Sig header.
func (r Record) Build() (headers map[string][]string, payload []byte, err error) {
	if err := r.Validate(); err != nil {
		return nil, nil, err
	}

	h := map[string][]string{
		HeaderMsgID:   {r.ID},
		HeaderVersion: {strconv.Itoa(Version)},
		HeaderAuthor:  {r.Author},
		HeaderType:    {r.Type},
		HeaderTs:      {r.Timestamp.Format(time.RFC3339Nano)},
	}
	if len(r.Parents) > 0 {
		h[HeaderParents] = []string{strings.Join(r.Parents, ",")}
	}
	if r.Signature != "" {
		h[HeaderSig] = []string{r.Signature}
	}
	for k, v := range r.Extras {
		h[k] = []string{v}
	}

	return h, r.Payload, nil
}

// Parse reads a wire message back into a Record. It enforces the required fields,
// Version == 1, a well-formed author, an RFC 3339 timestamp, and the absent-vs-empty
// parents rule, and preserves unknown Soulstream-* headers into Extras. It returns a
// *FieldError naming the first violation.
func Parse(headers map[string][]string, payload []byte) (Record, error) {
	get := func(k string) string {
		if v, ok := headers[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}

	var r Record

	r.ID = get(HeaderMsgID)
	if r.ID == "" {
		return Record{}, missingField(HeaderMsgID)
	}
	if _, err := uuid.Parse(r.ID); err != nil {
		return Record{}, badID(HeaderMsgID, "is not a valid uuid")
	}

	versionStr := get(HeaderVersion)
	if versionStr == "" {
		return Record{}, missingField(HeaderVersion)
	}
	version, err := strconv.Atoi(versionStr)
	if err != nil {
		return Record{}, badVersion(HeaderVersion, "is not an integer: "+versionStr)
	}
	if version != Version {
		return Record{}, badVersion(HeaderVersion, "unsupported version "+versionStr)
	}

	r.Author = get(HeaderAuthor)
	if r.Author == "" {
		return Record{}, missingField(HeaderAuthor)
	}
	if err := identity.CheckName(r.Author); err != nil {
		return Record{}, badAuthor(HeaderAuthor, err.Error())
	}

	r.Type = get(HeaderType)
	if r.Type == "" {
		return Record{}, missingField(HeaderType)
	}

	tsStr := get(HeaderTs)
	if tsStr == "" {
		return Record{}, missingField(HeaderTs)
	}
	ts, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return Record{}, badTimestamp(HeaderTs, "is not RFC 3339: "+tsStr)
	}
	r.Timestamp = ts

	if parentsStr := get(HeaderParents); parentsStr != "" {
		parts := strings.Split(parentsStr, ",")
		r.Parents = make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if _, err := uuid.Parse(p); err != nil {
				return Record{}, badID(HeaderParents, "parent is not a valid uuid: "+p)
			}
			r.Parents = append(r.Parents, p)
		}
	}

	r.Signature = get(HeaderSig)

	for k, v := range headers {
		if strings.HasPrefix(k, headerPrefix) && !knownHeader(k) && len(v) > 0 {
			if r.Extras == nil {
				r.Extras = map[string]string{}
			}
			r.Extras[k] = v[0]
		}
	}

	r.Payload = payload
	return r, nil
}

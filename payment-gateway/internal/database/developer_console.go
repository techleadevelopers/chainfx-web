package database

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type DeveloperProject struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	Environment       string          `json:"environment"`
	Status            string          `json:"status"`
	SpendingLimitUSDT string          `json:"spendingLimitUsdt"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	APIKeyCount       int             `json:"apiKeyCount"`
	AgentCount        int             `json:"agentCount"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type DeveloperProjectInput struct {
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Environment       string         `json:"environment"`
	Status            string         `json:"status"`
	SpendingLimitUSDT string         `json:"spendingLimitUsdt"`
	Metadata          map[string]any `json:"metadata"`
}

type DeveloperAPIKey struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"projectId"`
	Name               string          `json:"name"`
	Environment        string          `json:"environment"`
	PublicKey          string          `json:"publicKey"`
	MaskedSecret       string          `json:"maskedSecret"`
	LogHash            string          `json:"logHash"`
	Status             string          `json:"status"`
	Scopes             json.RawMessage `json:"scopes"`
	IPRestrictions     json.RawMessage `json:"ipRestrictions"`
	RateLimitPerMinute int             `json:"rateLimitPerMinute"`
	SpendingLimitUSDT  string          `json:"spendingLimitUsdt"`
	ExpiresAt          *time.Time      `json:"expiresAt,omitempty"`
	LastUsedAt         *time.Time      `json:"lastUsedAt,omitempty"`
	RotatedAt          *time.Time      `json:"rotatedAt,omitempty"`
	RevokedAt          *time.Time      `json:"revokedAt,omitempty"`
	Requests           int             `json:"requests"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

type DeveloperAPIKeyInput struct {
	Name               string   `json:"name"`
	Environment        string   `json:"environment"`
	ExpirationDays     int      `json:"expirationDays"`
	IPRestrictions     []string `json:"ipRestrictions"`
	Scopes             []string `json:"scopes"`
	RateLimitPerMinute int      `json:"rateLimitPerMinute"`
	SpendingLimitUSDT  string   `json:"spendingLimitUsdt"`
}

type DeveloperAPIKeyCreated struct {
	Key       *DeveloperAPIKey `json:"key"`
	SecretKey string           `json:"secretKey"`
	Warning   string           `json:"warning"`
}

type DeveloperAPIKeyValidation struct {
	KeyID       string
	ProjectID   string
	Environment string
	Status      string
	Scopes      []string
	LogHash     string
}

func (db *DB) CreateDeveloperProject(ctx context.Context, in DeveloperProjectInput) (*DeveloperProject, error) {
	env := normalizeDeveloperEnvironment(in.Environment)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("project name is required")
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "archived" && status != "disabled" {
		return nil, fmt.Errorf("invalid project status")
	}
	metadata, _ := json.Marshal(in.Metadata)
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = []byte(`{}`)
	}
	id := "proj_" + strings.ReplaceAll(NewID(), "-", "")
	row := db.SQL.QueryRowContext(ctx, `
		INSERT INTO developer_projects (id, name, description, environment, status, spending_limit_usdt, metadata_json)
		VALUES ($1,$2,$3,$4,$5,COALESCE(NULLIF($6,'')::numeric, 0),$7)
		RETURNING id, name, description, environment, status, spending_limit_usdt::text,
		          COALESCE(metadata_json, '{}'::jsonb), 0, 0, created_at, updated_at`,
		id, name, strings.TrimSpace(in.Description), env, status, strings.TrimSpace(in.SpendingLimitUSDT), json.RawMessage(metadata))
	return scanDeveloperProject(row)
}

func (db *DB) UpdateDeveloperProject(ctx context.Context, id string, in DeveloperProjectInput) (*DeveloperProject, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("project id is required")
	}
	env := normalizeDeveloperEnvironment(in.Environment)
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if status == "" {
		status = "active"
	}
	metadata, _ := json.Marshal(in.Metadata)
	if len(metadata) == 0 || string(metadata) == "null" {
		metadata = []byte(`{}`)
	}
	row := db.SQL.QueryRowContext(ctx, `
		UPDATE developer_projects
		SET name = COALESCE(NULLIF($2,''), name),
		    description = $3,
		    environment = $4,
		    status = $5,
		    spending_limit_usdt = COALESCE(NULLIF($6,'')::numeric, spending_limit_usdt),
		    metadata_json = $7,
		    updated_at = now()
		WHERE id = $1
		RETURNING id, name, description, environment, status, spending_limit_usdt::text,
		          COALESCE(metadata_json, '{}'::jsonb),
		          (SELECT COUNT(*)::int FROM developer_api_keys WHERE project_id = developer_projects.id),
		          (SELECT COUNT(*)::int FROM developer_project_agents WHERE project_id = developer_projects.id),
		          created_at, updated_at`,
		id, strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), env, status,
		strings.TrimSpace(in.SpendingLimitUSDT), json.RawMessage(metadata))
	project, err := scanDeveloperProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return project, err
}

func (db *DB) ListDeveloperProjects(ctx context.Context, limit int) ([]*DeveloperProject, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := db.SQL.QueryContext(ctx, `
		SELECT p.id, p.name, p.description, p.environment, p.status, p.spending_limit_usdt::text,
		       COALESCE(p.metadata_json, '{}'::jsonb),
		       (SELECT COUNT(*)::int FROM developer_api_keys k WHERE k.project_id = p.id),
		       (SELECT COUNT(*)::int FROM developer_project_agents a WHERE a.project_id = p.id),
		       p.created_at, p.updated_at
		FROM developer_projects p
		ORDER BY p.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DeveloperProject{}
	for rows.Next() {
		project, err := scanDeveloperProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, project)
	}
	return out, rows.Err()
}

func (db *DB) GetDeveloperProject(ctx context.Context, id string) (*DeveloperProject, error) {
	row := db.SQL.QueryRowContext(ctx, `
		SELECT p.id, p.name, p.description, p.environment, p.status, p.spending_limit_usdt::text,
		       COALESCE(p.metadata_json, '{}'::jsonb),
		       (SELECT COUNT(*)::int FROM developer_api_keys k WHERE k.project_id = p.id),
		       (SELECT COUNT(*)::int FROM developer_project_agents a WHERE a.project_id = p.id),
		       p.created_at, p.updated_at
		FROM developer_projects p
		WHERE p.id = $1`, strings.TrimSpace(id))
	project, err := scanDeveloperProject(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return project, err
}

func (db *DB) CreateDeveloperAPIKey(ctx context.Context, projectID string, in DeveloperAPIKeyInput) (*DeveloperAPIKeyCreated, error) {
	project, err := db.GetDeveloperProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if project == nil {
		return nil, fmt.Errorf("project not found")
	}
	env := normalizeDeveloperEnvironment(firstNonEmptyDB(in.Environment, project.Environment))
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("key name is required")
	}
	if len(in.Scopes) == 0 {
		in.Scopes = defaultDeveloperScopes()
	}
	if in.RateLimitPerMinute <= 0 {
		in.RateLimitPerMinute = 600
	}
	publicKey, secretKey, err := generateDeveloperAPIKeyPair(env)
	if err != nil {
		return nil, err
	}
	var expiresAt any
	if in.ExpirationDays > 0 {
		expiresAt = time.Now().UTC().Add(time.Duration(in.ExpirationDays) * 24 * time.Hour)
	}
	scopes, _ := json.Marshal(cleanStringList(in.Scopes))
	ipRestrictions, _ := json.Marshal(cleanStringList(in.IPRestrictions))
	key := &DeveloperAPIKey{
		ID:                 "key_" + strings.ReplaceAll(NewID(), "-", ""),
		ProjectID:          project.ID,
		Name:               name,
		Environment:        env,
		PublicKey:          publicKey,
		MaskedSecret:       maskDeveloperSecret(secretKey),
		LogHash:            DeveloperKeyLogHash(secretKey),
		Status:             "active",
		Scopes:             json.RawMessage(scopes),
		IPRestrictions:     json.RawMessage(ipRestrictions),
		RateLimitPerMinute: in.RateLimitPerMinute,
		SpendingLimitUSDT:  strings.TrimSpace(in.SpendingLimitUSDT),
	}
	if key.SpendingLimitUSDT == "" {
		key.SpendingLimitUSDT = "0"
	}
	_, err = db.SQL.ExecContext(ctx, `
		INSERT INTO developer_api_keys (
		  id, project_id, name, environment, public_key, secret_key_hash, log_hash,
		  status, scopes_json, ip_restrictions_json, rate_limit_per_minute,
		  spending_limit_usdt, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,'active',$8,$9,$10,COALESCE(NULLIF($11,'')::numeric, 0),$12)`,
		key.ID, key.ProjectID, key.Name, key.Environment, key.PublicKey, db.accessTokenHash(secretKey), key.LogHash,
		key.Scopes, key.IPRestrictions, key.RateLimitPerMinute, key.SpendingLimitUSDT, expiresAt)
	if err != nil {
		return nil, err
	}
	created, err := db.GetDeveloperAPIKey(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	return &DeveloperAPIKeyCreated{
		Key:       created,
		SecretKey: secretKey,
		Warning:   "Copy this secret now. You will not be able to view it again.",
	}, nil
}

func (db *DB) ListDeveloperAPIKeys(ctx context.Context, projectID string, limit int) ([]*DeveloperAPIKey, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	args := []any{}
	where := ""
	if strings.TrimSpace(projectID) != "" {
		args = append(args, strings.TrimSpace(projectID))
		where = "WHERE k.project_id = $1"
	}
	args = append(args, limit)
	rows, err := db.SQL.QueryContext(ctx, developerAPIKeySelect()+`
		`+where+`
		ORDER BY k.created_at DESC
		LIMIT $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DeveloperAPIKey{}
	for rows.Next() {
		key, err := scanDeveloperAPIKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (db *DB) GetDeveloperAPIKey(ctx context.Context, id string) (*DeveloperAPIKey, error) {
	row := db.SQL.QueryRowContext(ctx, developerAPIKeySelect()+` WHERE k.id = $1`, strings.TrimSpace(id))
	key, err := scanDeveloperAPIKey(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return key, err
}

func (db *DB) RotateDeveloperAPIKey(ctx context.Context, id string) (*DeveloperAPIKeyCreated, error) {
	key, err := db.GetDeveloperAPIKey(ctx, id)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("api key not found")
	}
	_, secretKey, err := generateDeveloperAPIKeyPair(key.Environment)
	if err != nil {
		return nil, err
	}
	_, err = db.SQL.ExecContext(ctx, `
		UPDATE developer_api_keys
		SET secret_key_hash = $2, log_hash = $3, rotated_at = now(), updated_at = now(), status = 'active'
		WHERE id = $1`, key.ID, db.accessTokenHash(secretKey), DeveloperKeyLogHash(secretKey))
	if err != nil {
		return nil, err
	}
	rotated, err := db.GetDeveloperAPIKey(ctx, key.ID)
	if err != nil {
		return nil, err
	}
	return &DeveloperAPIKeyCreated{Key: rotated, SecretKey: secretKey, Warning: "Copy this rotated secret now. You will not be able to view it again."}, nil
}

func (db *DB) SetDeveloperAPIKeyStatus(ctx context.Context, id, status string) (*DeveloperAPIKey, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "active" && status != "disabled" && status != "revoked" {
		return nil, fmt.Errorf("invalid api key status")
	}
	_, err := db.SQL.ExecContext(ctx, `
		UPDATE developer_api_keys
		SET status = $2,
		    revoked_at = CASE WHEN $2 = 'revoked' THEN now() ELSE revoked_at END,
		    updated_at = now()
		WHERE id = $1`, strings.TrimSpace(id), status)
	if err != nil {
		return nil, err
	}
	return db.GetDeveloperAPIKey(ctx, id)
}

func (db *DB) ValidateDeveloperAPIKey(ctx context.Context, secretKey string) (*DeveloperAPIKeyValidation, error) {
	secretKey = strings.TrimSpace(secretKey)
	if secretKey == "" {
		return nil, nil
	}
	hash := db.accessTokenHash(secretKey)
	var scopesRaw json.RawMessage
	out := &DeveloperAPIKeyValidation{}
	err := db.SQL.QueryRowContext(ctx, `
		SELECT id, project_id, environment, status, COALESCE(scopes_json, '[]'::jsonb), log_hash
		FROM developer_api_keys
		WHERE secret_key_hash = $1
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > now())`, hash).
		Scan(&out.KeyID, &out.ProjectID, &out.Environment, &out.Status, &scopesRaw, &out.LogHash)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(scopesRaw, &out.Scopes)
	_, _ = db.SQL.ExecContext(ctx, `UPDATE developer_api_keys SET last_used_at = now(), updated_at = now() WHERE id = $1`, out.KeyID)
	return out, nil
}

func developerAPIKeySelect() string {
	return `
		SELECT k.id, k.project_id, k.name, k.environment, k.public_key, k.log_hash, k.status,
		       COALESCE(k.scopes_json, '[]'::jsonb), COALESCE(k.ip_restrictions_json, '[]'::jsonb),
		       k.rate_limit_per_minute, k.spending_limit_usdt::text,
		       k.expires_at, k.last_used_at, k.rotated_at, k.revoked_at,
		       COALESCE((SELECT COUNT(*)::int FROM api_request_logs l WHERE l.api_key_hash = k.log_hash), 0),
		       k.created_at, k.updated_at
		FROM developer_api_keys k`
}

func scanDeveloperProject(row rowScanner) (*DeveloperProject, error) {
	item := &DeveloperProject{}
	if err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Environment, &item.Status,
		&item.SpendingLimitUSDT, &item.Metadata, &item.APIKeyCount, &item.AgentCount,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	return item, nil
}

func scanDeveloperAPIKey(row rowScanner) (*DeveloperAPIKey, error) {
	item := &DeveloperAPIKey{}
	var expiresAt, lastUsedAt, rotatedAt, revokedAt sql.NullTime
	if err := row.Scan(&item.ID, &item.ProjectID, &item.Name, &item.Environment, &item.PublicKey,
		&item.LogHash, &item.Status, &item.Scopes, &item.IPRestrictions, &item.RateLimitPerMinute,
		&item.SpendingLimitUSDT, &expiresAt, &lastUsedAt, &rotatedAt, &revokedAt, &item.Requests,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	item.MaskedSecret = maskDeveloperSecretPrefix(item.Environment)
	if expiresAt.Valid {
		item.ExpiresAt = &expiresAt.Time
	}
	if lastUsedAt.Valid {
		item.LastUsedAt = &lastUsedAt.Time
	}
	if rotatedAt.Valid {
		item.RotatedAt = &rotatedAt.Time
	}
	if revokedAt.Valid {
		item.RevokedAt = &revokedAt.Time
	}
	return item, nil
}

func generateDeveloperAPIKeyPair(environment string) (string, string, error) {
	env := normalizeDeveloperEnvironment(environment)
	pubPrefix := "pk_test_cfx_"
	secretPrefix := "sk_test_cfx_"
	if env == "production" {
		pubPrefix = "pk_live_cfx_"
		secretPrefix = "sk_live_cfx_"
	}
	publicToken, err := randomTokenHex(18)
	if err != nil {
		return "", "", err
	}
	secretToken, err := randomTokenHex(32)
	if err != nil {
		return "", "", err
	}
	return pubPrefix + publicToken, secretPrefix + secretToken, nil
}

func randomTokenHex(bytesLen int) (string, error) {
	raw := make([]byte, bytesLen)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func DeveloperKeyLogHash(secretKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(secretKey)))
	return hex.EncodeToString(sum[:])[:16]
}

func normalizeDeveloperEnvironment(value string) string {
	env := strings.ToLower(strings.TrimSpace(value))
	switch env {
	case "production", "prod", "live":
		return "production"
	default:
		return "sandbox"
	}
}

func defaultDeveloperScopes() []string {
	return []string{"rates:read", "orders:read", "orders:create", "capabilities:read", "capabilities:purchase", "capabilities:execute", "mcp:connect", "webhooks:write"}
}

func cleanStringList(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			item := strings.TrimSpace(part)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func maskDeveloperSecret(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) <= 14 {
		return "sk_************************"
	}
	return secret[:10] + "..." + secret[len(secret)-4:]
}

func maskDeveloperSecretPrefix(environment string) string {
	if normalizeDeveloperEnvironment(environment) == "production" {
		return "sk_live_cfx_************************"
	}
	return "sk_test_cfx_************************"
}

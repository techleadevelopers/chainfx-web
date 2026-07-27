package models

import (
	"time"
)

// ─── User ────────────────────────────────────────────────────────────────────

type KYCStatus string

const (
	KYCPending   KYCStatus = "pending"
	KYCSubmitted KYCStatus = "submitted"
	KYCApproved  KYCStatus = "approved"
	KYCRejected  KYCStatus = "rejected"
)

type User struct {
	ID                  string    `json:"id"                          db:"id"`
	Email               string    `json:"email"                        db:"email"`
	Phone               *string   `json:"phone,omitempty"              db:"phone"`
	FullName            *string   `json:"full_name,omitempty"          db:"full_name"`
	CPF                 *string   `json:"cpf,omitempty"                db:"cpf"`
	BirthDate           *string   `json:"birth_date,omitempty"         db:"birth_date"`
	AddressPostalCode   *string   `json:"address_postal_code,omitempty" db:"address_postal_code"`
	AddressStreet       *string   `json:"address_street,omitempty"     db:"address_street"`
	AddressNumber       *string   `json:"address_number,omitempty"     db:"address_number"`
	AddressNeighborhood *string   `json:"address_neighborhood,omitempty" db:"address_neighborhood"`
	AddressCity         *string   `json:"address_city,omitempty"       db:"address_city"`
	AddressState        *string   `json:"address_state,omitempty"      db:"address_state"`
	AddressCountry      *string   `json:"address_country,omitempty"    db:"address_country"`
	AvatarURL           *string   `json:"avatar_url,omitempty"         db:"avatar_url"`
	PasswordHash        string    `json:"-"                            db:"password_hash"`
	WalletAddress       *string   `json:"wallet_address,omitempty"     db:"wallet_address"`
	PixKey              *string   `json:"pix_key,omitempty"            db:"pix_key"`
	KYCStatus           KYCStatus `json:"kyc_status"                   db:"kyc_status"`
	KYCDocuments        *string   `json:"kyc_documents,omitempty"      db:"kyc_documents"`
	PinHash             *string   `json:"-"                            db:"pin_hash"`
	BiometryEnabled     bool      `json:"biometry_enabled"             db:"biometry_enabled"`
	TwoFactorEnabled    bool      `json:"two_factor_enabled"           db:"two_factor_enabled"`
	TwoFactorSecret     *string   `json:"-"                            db:"two_factor_secret"`
	RefreshTokenHash    *string   `json:"-"                            db:"refresh_token_hash"`
	CreatedAt           time.Time `json:"created_at"                   db:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"                   db:"updated_at"`
}

// ─── Device ──────────────────────────────────────────────────────────────────

type Device struct {
	ID         string     `json:"id"                     db:"id"`
	UserID     string     `json:"user_id"                db:"user_id"`
	DeviceName *string    `json:"device_name,omitempty"  db:"device_name"`
	DeviceType *string    `json:"device_type,omitempty"  db:"device_type"`
	FCMToken   *string    `json:"fcm_token,omitempty"    db:"fcm_token"`
	APNSToken  *string    `json:"apns_token,omitempty"   db:"apns_token"`
	LastActive *time.Time `json:"last_active,omitempty"  db:"last_active"`
	CreatedAt  time.Time  `json:"created_at"             db:"created_at"`
}

// ─── DCAStrategy ─────────────────────────────────────────────────────────────

type DCAFrequency string

const (
	DCADaily   DCAFrequency = "daily"
	DCAWeekly  DCAFrequency = "weekly"
	DCAMonthly DCAFrequency = "monthly"
)

type DCAStrategy struct {
	ID            string       `json:"id"             db:"id"`
	UserID        string       `json:"user_id"        db:"user_id"`
	TokenSymbol   string       `json:"token_symbol"   db:"token_symbol"`
	Network       string       `json:"network"        db:"network"`
	AmountBRL     float64      `json:"amount_brl"     db:"amount_brl"`
	Frequency     DCAFrequency `json:"frequency"      db:"frequency"`
	Active        bool         `json:"active"         db:"active"`
	TotalInvested float64      `json:"total_invested" db:"total_invested"`
	TotalTokens   float64      `json:"total_tokens"   db:"total_tokens"`
	NextExecution *time.Time   `json:"next_execution" db:"next_execution"`
	CreatedAt     time.Time    `json:"created_at"     db:"created_at"`
}

// ─── Notification ─────────────────────────────────────────────────────────────

type Notification struct {
	ID        string    `json:"id"               db:"id"`
	UserID    string    `json:"user_id"          db:"user_id"`
	Title     string    `json:"title"            db:"title"`
	Body      *string   `json:"body,omitempty"   db:"body"`
	Type      *string   `json:"type,omitempty"   db:"type"`
	Read      bool      `json:"read"             db:"read"`
	Data      *string   `json:"data,omitempty"   db:"data"`
	CreatedAt time.Time `json:"created_at"       db:"created_at"`
}

// ─── Settings ─────────────────────────────────────────────────────────────────

type UserSettings struct {
	UserID               string  `json:"user_id"               db:"user_id"`
	DarkMode             bool    `json:"dark_mode"             db:"dark_mode"`
	Language             string  `json:"language"              db:"language"`
	Currency             string  `json:"currency"              db:"currency"`
	NotificationsEnabled bool    `json:"notifications_enabled" db:"notifications_enabled"`
	DailyLimit           float64 `json:"daily_limit"           db:"daily_limit"`
}

package profile

import (
	"errors"
	"slices"

	"github.com/zalando/go-keyring"
)

type SecretStore interface {
	Get(id string) (string, error)
	Set(id, password string) error
	Delete(id string) error
}

type SystemKeychain struct{}

const service = "quick-query"

func (SystemKeychain) Get(id string) (string, error) { return keyring.Get(service, id) }
func (SystemKeychain) Set(id, password string) error { return keyring.Set(service, id, password) }
func (SystemKeychain) Delete(id string) error {
	err := keyring.Delete(service, id)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

type Manager struct {
	Profiles Repository
	Secrets  SecretStore
}

// Password never consults the keychain for profiles explicitly set to prompt.
func (m Manager) Password(p Profile) (password, explanation string, prompt bool) {
	if p.PasswordMode == Prompt {
		return "", "Password is used only for this connection.", true
	}
	password, err := m.Secrets.Get(p.ID)
	if err != nil || password == "" {
		return "", "Saved password unavailable. Enter it for this connection only.", true
	}
	return password, "", false
}

// Save updates secrets before the atomic profile write, rolling them back on a
// config failure when possible. Failed keychain writes fall back to prompt mode.
func (m Manager) Save(p Profile, password string) ([]Profile, string, error) {
	profiles, err := m.Profiles.Load()
	if err != nil {
		return nil, "", err
	}
	p.PasswordMode = Prompt
	if password != "" {
		p.PasswordMode = Keychain
	}
	if err := p.Validate(); err != nil {
		return nil, "", err
	}
	i := slices.IndexFunc(profiles, func(existing Profile) bool { return existing.ID == p.ID })
	hadSecret := i >= 0 && profiles[i].PasswordMode == Keychain
	oldPassword, oldErr := "", errors.New("no saved password")
	if hadSecret {
		oldPassword, oldErr = m.Secrets.Get(p.ID)
	}
	secretChanged := false
	warning := ""
	if password == "" {
		if hadSecret {
			if err := m.Secrets.Delete(p.ID); err != nil {
				warning = "Profile will prompt each time; the old keychain entry could not be removed."
			} else {
				secretChanged = true
			}
		}
	} else if err := m.Secrets.Set(p.ID, password); err != nil {
		p.PasswordMode = Prompt
		warning = "Keychain unavailable. Profile saved with a password prompt on each connection."
	} else {
		secretChanged = true
	}
	if i < 0 {
		profiles = append(profiles, p)
	} else {
		profiles[i] = p
	}
	if err := m.Profiles.Save(profiles); err != nil {
		if !secretChanged {
			return nil, "", err
		}
		var rollbackErr error
		if oldErr == nil {
			rollbackErr = m.Secrets.Set(p.ID, oldPassword)
		} else {
			rollbackErr = m.Secrets.Delete(p.ID)
		}
		if rollbackErr != nil || (hadSecret && oldErr != nil) {
			return nil, "", errors.Join(err, errors.New("keychain rollback failed; re-enter the password when reconnecting"))
		}
		return nil, "", err
	}
	return profiles, warning, nil
}

func (m Manager) Delete(id string) ([]Profile, string, error) {
	profiles, err := m.Profiles.Load()
	if err != nil {
		return nil, "", err
	}
	profiles = slices.DeleteFunc(profiles, func(p Profile) bool { return p.ID == id })
	if err := m.Profiles.Save(profiles); err != nil {
		return nil, "", err
	}
	warning := ""
	if err := m.Secrets.Delete(id); err != nil {
		warning = "Profile deleted; its keychain entry could not be removed."
	}
	return profiles, warning, nil
}

package config

import (
	"context"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/samber/do/v2"
)

// Vault provides the secret store on the node's key (ADR-0077): the
// operator's key from the environment, else the key file at
// Bootstrap.MasterKey, made at the first start. A key that cannot be used
// stops the start; one that does not fit the stored secrets opens the
// store locked.
func Vault() do.Provider[*vault.Vault] {
	return func(i do.Injector) (*vault.Vault, error) {
		b := do.MustInvoke[Bootstrap](i)
		key, created, err := vault.LoadKey(moduleSecrets(b), b.MasterKey)
		if err != nil {
			return nil, err
		}
		v, err := vault.Open(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, key, time.Now)
		if err != nil {
			return nil, err
		}
		vault.Announce(do.MustInvoke[*slog.Logger](i), b.MasterKey, created, v.Locked())
		return v, nil
	}
}

// secretsFor is the secret source of each module: its own scope of the
// store, then the node's environment. No module reads another module's
// scope or the node's.
func secretsFor(b Bootstrap, v *vault.Vault) modhost.SecretsFor {
	env := moduleSecrets(b)
	return func(module string) secrets.Source {
		return secrets.First{v.Source(secrets.ModuleScope(module)), env}
	}
}

// forgetSecrets drops the stored secrets of a removed module.
func forgetSecrets(v *vault.Vault) modhost.ForgetSecrets {
	return func(ctx context.Context, module string) error {
		_, err := v.RemoveScope(ctx, secrets.ModuleScope(module))
		return err
	}
}

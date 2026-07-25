CREATE TABLE IF NOT EXISTS session (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- token guarda el SHA-256 (hex) del token, nunca el valor en crudo que
    -- tiene el cliente. Es unico para poder buscar la sesion por su hash.
    token        VARCHAR(64) NOT NULL UNIQUE,
    user_agent   VARCHAR(255),
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS session_user_id_idx ON session (user_id);

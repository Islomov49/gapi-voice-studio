-- A workspace is one Gapi project seen from the Studio: the calls go out
-- through that project's token, lines and balance.
create table workspaces (
    id                uuid primary key default gen_random_uuid(),
    -- What tells one Gapi project's token from another's: the public id inside it.
    gapi_key          text not null unique,
    name              text not null default '',
    gapi_token_sealed bytea not null,
    -- Who the agent calls first to show how it talks.
    tester_phone      text not null default '',
    -- Calls in progress at once, test calls included.
    channels          int not null default 3,
    created_at        timestamptz not null default now()
);

create table sessions (
    token_hash   bytea primary key,
    workspace_id uuid not null references workspaces (id) on delete cascade,
    created_at   timestamptz not null default now(),
    expires_at   timestamptz not null
);

create table agents (
    id           uuid primary key default gen_random_uuid(),
    workspace_id uuid not null references workspaces (id) on delete cascade,
    name         text not null,
    template     text not null default '',
    created_at   timestamptz not null default now(),
    archived_at  timestamptz
);

-- Every change of an agent's script or form is a new version; calls name the
-- version they were made with. accepted_at: a person heard a test call of
-- exactly this version and accepted it, which is what lets a campaign start.
create table agent_versions (
    id          uuid primary key default gen_random_uuid(),
    agent_id    uuid not null references agents (id) on delete cascade,
    n           int not null,
    spec        jsonb not null,
    author      text not null default 'user',
    note        text not null default '',
    accepted_at timestamptz,
    created_at  timestamptz not null default now(),
    unique (agent_id, n)
);

create table contact_lists (
    id           uuid primary key default gen_random_uuid(),
    workspace_id uuid not null references workspaces (id) on delete cascade,
    name         text not null,
    -- [{"key": "client_name", "title": "Имя"}]: the sheet's columns besides the phone.
    columns      jsonb not null default '[]',
    -- What the upload found: rows, accepted, duplicates, invalid, stopped.
    stats        jsonb not null default '{}',
    created_at   timestamptz not null default now(),
    archived_at  timestamptz
);

create table contacts (
    id      uuid primary key default gen_random_uuid(),
    list_id uuid not null references contact_lists (id) on delete cascade,
    row_no  int not null,
    phone   text not null,
    data    jsonb not null default '{}'
);
create index contacts_list on contacts (list_id, row_no);

create table campaigns (
    id               uuid primary key default gen_random_uuid(),
    workspace_id     uuid not null references workspaces (id) on delete cascade,
    name             text not null,
    agent_version_id uuid not null references agent_versions (id),
    list_id          uuid references contact_lists (id),
    -- The Gapi line the calls leave from (its id or number); empty with one line.
    line             text not null default '',
    rules            jsonb not null,
    -- draft, running, paused, done
    status           text not null default 'draft',
    pause_reason     text not null default '',
    -- The portion being called; 0 before the start.
    portion          int not null default 0,
    -- The portion whose stop a person has looked at and let pass: the guard
    -- that paused the campaign after it does not pause it again.
    guard_ok         int not null default 0,
    created_at       timestamptz not null default now(),
    started_at       timestamptz,
    finished_at      timestamptz
);

-- One person of a campaign. portion 0 = not handed out yet.
create table targets (
    id           uuid primary key default gen_random_uuid(),
    campaign_id  uuid not null references campaigns (id) on delete cascade,
    contact_id   uuid,
    row_no       int not null default 0,
    phone        text not null,
    vars         jsonb not null default '{}',
    portion      int not null default 0,
    -- pending (to be called, at next_at), calling, done (talked), failed (not reached), skipped (stop list)
    status       text not null default 'pending',
    attempts     int not null default 0,
    next_at      timestamptz,
    result       text not null default '',
    last_call_id uuid
);
create index targets_due on targets (campaign_id, status, next_at);

create table calls (
    id               uuid primary key default gen_random_uuid(),
    workspace_id     uuid not null references workspaces (id) on delete cascade,
    -- test, campaign
    kind             text not null,
    campaign_id      uuid references campaigns (id) on delete set null,
    target_id        uuid references targets (id) on delete set null,
    agent_version_id uuid not null references agent_versions (id),
    phone            text not null,
    gapi_call_id     text not null default '',
    -- created (ringing), active, ended (talked), failed (not reached, or not started)
    status           text not null default 'created',
    end_reason       text not null default '',
    outcome          text not null default '',
    duration_seconds float8,
    charged_g        float8,
    transcript       jsonb,
    -- {"field_key": {"value": ..., "quote": "...", "edited": false}}
    answers          jsonb not null default '{}',
    summary          text not null default '',
    -- What the checks of the call's text found: {"ai_disclosed": true, "do_not_call": false, ...}
    checks           jsonb not null default '{}',
    -- none, pending, done, failed
    analysis         text not null default 'none',
    -- A test call: the person's verdict and words.
    accepted         boolean,
    feedback         text not null default '',
    created_at       timestamptz not null default now(),
    started_at       timestamptz,
    ended_at         timestamptz,
    -- The dialer is done with the call: its end is read and its target moved on.
    settled_at       timestamptz
);
create index calls_recent on calls (workspace_id, created_at desc);
create index calls_open on calls (workspace_id) where settled_at is null;

create table stoplist (
    workspace_id uuid not null references workspaces (id) on delete cascade,
    phone        text not null,
    reason       text not null default '',
    -- manual, upload, call
    source       text not null default 'manual',
    call_id      uuid,
    created_at   timestamptz not null default now(),
    primary key (workspace_id, phone)
);

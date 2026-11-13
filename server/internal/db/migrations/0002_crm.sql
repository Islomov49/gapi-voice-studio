-- A person of a list may stand for a card in a CRM: ref names it
-- ("bitrix24:deal:123", "amocrm:lead:456", or whatever an assistant gave),
-- and a call's outcome is written back to that card.
alter table contacts add column ref text not null default '';
alter table targets add column ref text not null default '';

-- A CRM the workspace is connected to. kind: bitrix24, amocrm.
create table crm_connections (
    id            uuid primary key default gen_random_uuid(),
    workspace_id  uuid not null references workspaces (id) on delete cascade,
    kind          text not null,
    name          text not null default '',
    -- Bitrix24: the portal's address; amoCRM: the account's address.
    base          text not null,
    -- Bitrix24: the inbound webhook's path with its secret; amoCRM: the long-lived token.
    secret_sealed bytea not null,
    created_at    timestamptz not null default now()
);

-- Where a list came from, when it was read from a CRM's stage:
-- {"connection_id", "entity", "pipeline_id", "stage_id"}.
alter table contact_lists add column source jsonb;

-- What a campaign does in the CRM (crm.Rules): the connection, whether a note
-- is written after each call, the stage a card moves to and the task set for
-- each result, and the stage new cards are taken from while it runs.
alter table campaigns add column crm jsonb;
-- When the campaign last looked at its CRM stage for new cards.
alter table campaigns add column crm_fed_at timestamptz;

-- What still has to be written to the CRM for a call: '' nothing, 'call' the
-- note of the call (and the stage and the task), 'form' the form's answers,
-- 'done', 'failed' (crm_error says why).
alter table calls add column crm_state text not null default '';
alter table calls add column crm_error text not null default '';
create index calls_crm on calls (crm_state) where crm_state in ('call', 'form');

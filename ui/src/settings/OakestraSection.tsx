import {
  Alert,
  AlertDescription,
  Button,
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  Input,
  SectionCard,
  Skeleton,
  StatusBadge,
} from '@oakestra/ui';
import { useState, type FormEvent } from 'react';

import { useSaveOakestraCredentials, useStatus } from '../api/hooks';
import type { OakestraStatus } from '../api/types';
import { QueryError } from '../components/QueryError';
import { READ_ONLY_REASON, useCanWrite } from '../components/permissions';

function StatusSummary({ oakestra }: { oakestra: OakestraStatus }) {
  const rows: [string, string][] = [
    ['Source', oakestra.source === 'env' ? 'Environment variables' : oakestra.source === 'stored' ? 'Entered here' : '-'],
    ['Username', oakestra.username ?? '-'],
    ['Organization', oakestra.organization || '-'],
  ];
  return (
    <div className="ghr:space-y-3">
      <div>
        {!oakestra.configured ? (
          <StatusBadge tone="warning">Not configured</StatusBadge>
        ) : oakestra.ok ? (
          <StatusBadge tone="success">Login works</StatusBadge>
        ) : (
          <StatusBadge tone="destructive">Login failed</StatusBadge>
        )}
      </div>
      {oakestra.configured ? (
        <dl className="ghr:grid ghr:grid-cols-[max-content_1fr] ghr:gap-x-6 ghr:gap-y-1 ghr:text-sm">
          {rows.map(([label, value]) => (
            <div key={label} className="ghr:contents">
              <dt className="ghr:text-muted-foreground">{label}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      ) : null}
      {oakestra.configured && !oakestra.ok && oakestra.error ? (
        <Alert variant="destructive">
          <AlertDescription>{oakestra.error}</AlertDescription>
        </Alert>
      ) : null}
    </div>
  );
}

function CredentialsForm({ initial }: { initial: OakestraStatus }) {
  const canWrite = useCanWrite();
  const save = useSaveOakestraCredentials();
  const [username, setUsername] = useState(initial.username ?? '');
  const [password, setPassword] = useState('');
  const [organization, setOrganization] = useState(initial.organization ?? '');
  const [submitted, setSubmitted] = useState(false);

  const errors = {
    username: submitted && username.trim() === '' ? 'Enter the username.' : undefined,
    password: submitted && password === '' ? 'Enter the password.' : undefined,
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (username.trim() === '' || password === '') return;
    save.mutate(
      {
        username: username.trim(),
        password,
        organization: organization.trim() || undefined,
      },
      { onSuccess: () => setPassword('') },
    );
  };

  return (
    <form onSubmit={onSubmit} noValidate>
      <FieldGroup>
        <Alert variant="info">
          <AlertDescription>
            Use a separate Oakestra user for this addon, not your own account. Oakestra only shows
            services to the user who created them, so runners started under a personal account
            would be invisible to everyone else. An admin can create the user on the Users page.
          </AlertDescription>
        </Alert>
        <Field data-invalid={!!errors.username}>
          <FieldLabel htmlFor="oak-username">Username</FieldLabel>
          <Input
            id="oak-username"
            value={username}
            autoComplete="off"
            disabled={!canWrite}
            aria-invalid={!!errors.username}
            onChange={(event) => setUsername(event.target.value)}
          />
          <FieldError errors={[errors.username ? { message: errors.username } : undefined]} />
        </Field>
        <Field data-invalid={!!errors.password}>
          <FieldLabel htmlFor="oak-password">Password</FieldLabel>
          <Input
            id="oak-password"
            type="password"
            value={password}
            autoComplete="new-password"
            disabled={!canWrite}
            aria-invalid={!!errors.password}
            onChange={(event) => setPassword(event.target.value)}
          />
          <FieldError errors={[errors.password ? { message: errors.password } : undefined]} />
        </Field>
        <Field>
          <FieldLabel htmlFor="oak-org">Organization</FieldLabel>
          <Input
            id="oak-org"
            value={organization}
            autoComplete="off"
            disabled={!canWrite}
            onChange={(event) => setOrganization(event.target.value)}
          />
          <FieldDescription>Optional. Leave empty for the root organization.</FieldDescription>
        </Field>
        <div>
          <Button type="submit" disabled={!canWrite || save.isPending} title={canWrite ? undefined : READ_ONLY_REASON}>
            {save.isPending ? 'Saving...' : 'Save credentials'}
          </Button>
        </div>
      </FieldGroup>
    </form>
  );
}

export function OakestraSection() {
  const status = useStatus();
  const oakestra = status.data?.oakestra;

  return (
    <SectionCard title="Oakestra service user" description="The account the backend uses to create runner services.">
      {status.isError ? (
        <QueryError title="Could not load the status" error={status.error} onRetry={() => void status.refetch()} />
      ) : oakestra === undefined ? (
        <Skeleton className="ghr:h-24 ghr:w-full" />
      ) : (
        <div className="ghr:space-y-6">
          <StatusSummary oakestra={oakestra} />
          {oakestra.source === 'env' ? (
            <p className="ghr:text-sm ghr:text-muted-foreground">
              These credentials come from the backend's environment variables and cannot be changed
              here.
            </p>
          ) : (
            <CredentialsForm initial={oakestra} />
          )}
        </div>
      )}
    </SectionCard>
  );
}

import {
  Button,
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  Input,
  SectionCard,
} from '@oakestra/ui';
import { useState, type FormEvent } from 'react';

import { useCreateTokenConnection } from '../api/hooks';
import { READ_ONLY_REASON, useCanWrite } from '../components/permissions';
import { GITHUB_URL_HELP, GITHUB_URL_PATTERN } from '../lib/validation';

export function TokenForm() {
  const canWrite = useCanWrite();
  const create = useCreateTokenConnection();
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [token, setToken] = useState('');
  const [submitted, setSubmitted] = useState(false);

  const errors = {
    name: submitted && name.trim() === '' ? 'Enter a name.' : undefined,
    url: submitted && !GITHUB_URL_PATTERN.test(url.trim()) ? GITHUB_URL_HELP : undefined,
    token: submitted && token === '' ? 'Enter a token.' : undefined,
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (name.trim() === '' || !GITHUB_URL_PATTERN.test(url.trim()) || token === '') return;
    create.mutate(
      { name: name.trim(), config_url: url.trim(), token },
      {
        onSuccess: () => {
          setName('');
          setUrl('');
          setToken('');
          setSubmitted(false);
        },
      },
    );
  };

  return (
    <SectionCard
      title="Connect with a token"
      description="A personal access token works without creating a GitHub App. It is stored encrypted and never shown again."
    >
      <form onSubmit={onSubmit} noValidate>
        <FieldGroup>
          <Field data-invalid={!!errors.name}>
            <FieldLabel htmlFor="token-name">Name</FieldLabel>
            <Input
              id="token-name"
              value={name}
              autoComplete="off"
              disabled={!canWrite}
              aria-invalid={!!errors.name}
              onChange={(event) => setName(event.target.value)}
            />
            <FieldError errors={[errors.name ? { message: errors.name } : undefined]} />
          </Field>
          <Field data-invalid={!!errors.url}>
            <FieldLabel htmlFor="token-url">Organization or repository URL</FieldLabel>
            <Input
              id="token-url"
              value={url}
              placeholder="https://github.com/my-org/my-repo"
              autoComplete="off"
              spellCheck={false}
              disabled={!canWrite}
              aria-invalid={!!errors.url}
              onChange={(event) => setUrl(event.target.value)}
            />
            <FieldError errors={[errors.url ? { message: errors.url } : undefined]} />
          </Field>
          <Field data-invalid={!!errors.token}>
            <FieldLabel htmlFor="token-value">Token</FieldLabel>
            <Input
              id="token-value"
              type="password"
              value={token}
              autoComplete="new-password"
              disabled={!canWrite}
              aria-invalid={!!errors.token}
              onChange={(event) => setToken(event.target.value)}
            />
            <FieldDescription>
              Classic token: <code>repo</code> for a repository, <code>admin:org</code> for an
              organization. Fine-grained token: Administration read and write for a repository,
              Self-hosted runners read and write for an organization.
            </FieldDescription>
            <FieldError errors={[errors.token ? { message: errors.token } : undefined]} />
          </Field>
          <div>
            <Button type="submit" disabled={!canWrite || create.isPending} title={canWrite ? undefined : READ_ONLY_REASON}>
              {create.isPending ? 'Checking token...' : 'Connect'}
            </Button>
          </div>
        </FieldGroup>
      </form>
    </SectionCard>
  );
}

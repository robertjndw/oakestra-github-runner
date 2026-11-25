import { useNotify } from '@oakestra/dashboard-sdk';
import {
  Alert,
  AlertDescription,
  AlertTitle,
  Button,
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  Input,
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
  SectionCard,
} from '@oakestra/ui';
import { ExternalLinkIcon, RefreshCwIcon } from 'lucide-react';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useSearchParams } from 'react-router';

import { errorMessage } from '../api/client';
import {
  useCompleteAppManifest,
  useConnections,
  useInstallations,
  useSelectInstallation,
  useStartAppManifest,
} from '../api/hooks';
import type { Connection } from '../api/types';
import { READ_ONLY_REASON, useCanWrite } from '../components/permissions';
import { currentPageUrl, submitManifestForm } from '../lib/manifest-form';
import { GITHUB_URL_PATTERN, GITHUB_URL_HELP } from '../lib/validation';

function InstallationStep({ connection }: { connection: Connection }) {
  const installations = useInstallations(connection.id);
  const select = useSelectInstallation();
  const canWrite = useCanWrite();

  return (
    <Alert variant="warning" data-testid={`install-${connection.id}`}>
      <AlertTitle>Finish connecting {connection.name}</AlertTitle>
      <AlertDescription>
        <p>
          The GitHub App exists but is not installed yet. Install it on{' '}
          <span className="ghr:font-medium">{connection.config_url.replace(/^https:\/\/github\.com\//, '')}</span>,
          then choose the installation.
        </p>
        <div className="ghr:flex ghr:flex-wrap ghr:gap-2">
          {connection.app_html_url ? (
            <Button asChild size="sm">
              <a href={`${connection.app_html_url}/installations/new`} target="_blank" rel="noreferrer">
                Install the GitHub App <ExternalLinkIcon aria-hidden />
              </a>
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="outline"
            disabled={installations.isFetching}
            onClick={() => void installations.refetch()}
          >
            <RefreshCwIcon aria-hidden /> Refresh installations
          </Button>
        </div>
        {installations.isError ? (
          <p className="ghr:text-destructive">{errorMessage(installations.error)}</p>
        ) : installations.data && installations.data.length === 0 ? (
          <p>No installations found yet. Install the App, then refresh.</p>
        ) : installations.data ? (
          <ItemGroup className="ghr:w-full">
            {installations.data.map((installation) => (
              <Item key={installation.id} variant="outline" size="sm">
                <ItemContent>
                  <ItemTitle>{installation.account}</ItemTitle>
                  <ItemDescription>{installation.target_type}</ItemDescription>
                </ItemContent>
                <ItemActions>
                  <Button
                    size="sm"
                    disabled={!canWrite || select.isPending}
                    title={canWrite ? undefined : READ_ONLY_REASON}
                    onClick={() =>
                      select.mutate({ connectionId: connection.id, installationId: installation.id })
                    }
                  >
                    Use this installation
                  </Button>
                </ItemActions>
              </Item>
            ))}
          </ItemGroup>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

export function AppWizard() {
  const canWrite = useCanWrite();
  const notify = useNotify();
  const connections = useConnections();
  const start = useStartAppManifest();
  const complete = useCompleteAppManifest();
  const [params, setParams] = useSearchParams();

  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [redirecting, setRedirecting] = useState(false);

  // The code is single-use, so React's double effect run in development must not call twice.
  const handledCallback = useRef(false);
  useEffect(() => {
    const code = params.get('code');
    const state = params.get('state');
    if (!code || !state || handledCallback.current) return;
    handledCallback.current = true;
    // Strip first: a reload must not replay the exchange.
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        next.delete('code');
        next.delete('state');
        return next;
      },
      { replace: true },
    );
    complete.mutate(
      { code, state },
      {
        onSuccess: () => notify.info('Now install the GitHub App to finish the connection.'),
      },
    );
  }, [params, setParams, complete, notify]);

  const nameError = submitted && name.trim() === '' ? 'Enter a name.' : undefined;
  const urlError = submitted && !GITHUB_URL_PATTERN.test(url.trim()) ? GITHUB_URL_HELP : undefined;

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    setSubmitted(true);
    if (name.trim() === '' || !GITHUB_URL_PATTERN.test(url.trim())) return;
    start.mutate(
      { name: name.trim(), config_url: url.trim(), redirect_url: currentPageUrl() },
      {
        onSuccess: (manifest) => {
          setRedirecting(true);
          submitManifestForm(manifest.post_url, manifest.manifest);
        },
      },
    );
  };

  const pending = (connections.data ?? []).filter((connection) => connection.status === 'pending_installation');

  return (
    <SectionCard
      title="Connect with a GitHub App"
      description="Recommended. GitHub creates an App for you after you confirm, and the backend keeps its key."
    >
      <div className="ghr:space-y-4">
        {complete.isPending ? <p className="ghr:text-sm ghr:text-muted-foreground">Finishing the GitHub App setup...</p> : null}
        {pending.map((connection) => (
          <InstallationStep key={connection.id} connection={connection} />
        ))}
        <ol className="ghr:list-inside ghr:list-decimal ghr:space-y-1 ghr:text-sm ghr:text-muted-foreground">
          <li>Fill in the two fields below. GitHub opens with the App prepared, you only confirm it.</li>
          <li>GitHub sends you back to this page.</li>
          <li>Install the App on the organization or repository, then pick that installation here.</li>
        </ol>
        <p className="ghr:text-sm ghr:text-muted-foreground">
          With an organization URL, every repository the runner group allows can use the pools. With
          a repository URL, only that repository can. The App asks for one permission: Self-hosted
          runners for an organization, Administration for a single repository, because GitHub has
          no narrower one for repository runners.
        </p>
        <form onSubmit={onSubmit} noValidate>
          <FieldGroup>
            <Field data-invalid={!!nameError}>
              <FieldLabel htmlFor="app-name">Name</FieldLabel>
              <Input
                id="app-name"
                value={name}
                autoComplete="off"
                disabled={!canWrite}
                aria-invalid={!!nameError}
                onChange={(event) => setName(event.target.value)}
              />
              <FieldDescription>Shown in the list of connections and as the name of the GitHub App.</FieldDescription>
              <FieldError errors={[nameError ? { message: nameError } : undefined]} />
            </Field>
            <Field data-invalid={!!urlError}>
              <FieldLabel htmlFor="app-url">Organization or repository URL</FieldLabel>
              <Input
                id="app-url"
                value={url}
                placeholder="https://github.com/my-org"
                autoComplete="off"
                spellCheck={false}
                disabled={!canWrite}
                aria-invalid={!!urlError}
                onChange={(event) => setUrl(event.target.value)}
              />
              <FieldError errors={[urlError ? { message: urlError } : undefined]} />
            </Field>
            <div>
              <Button
                type="submit"
                disabled={!canWrite || start.isPending || redirecting}
                title={canWrite ? undefined : READ_ONLY_REASON}
              >
                {start.isPending || redirecting ? 'Opening GitHub...' : 'Continue on GitHub'}
              </Button>
            </div>
          </FieldGroup>
        </form>
      </div>
    </SectionCard>
  );
}

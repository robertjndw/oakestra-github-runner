import './addon.css';

import { defineAddon } from '@oakestra/dashboard-sdk';
import { ContainerIcon } from 'lucide-react';
import { lazy } from 'react';
import { ADDON_ID } from 'virtual:oakestra-addon';

import { BusyRunnersWidget } from './widgets/BusyRunnersWidget';
import { ClusterRunnersWidget } from './widgets/ClusterRunnersWidget';

// The shell loads this module on every page for the nav item and the widgets, so the pages stay
// behind dynamic imports.
const OverviewPage = lazy(() => import('./pages/OverviewPage'));
const RunnersPage = lazy(() => import('./pages/RunnersPage'));
const JobsPage = lazy(() => import('./pages/JobsPage'));
const PoolsPage = lazy(() => import('./pages/PoolsPage'));
const SettingsPage = lazy(() => import('./pages/SettingsPage'));

export default defineAddon({
  id: ADDON_ID,
  // First breadcrumb of the addon's pages.
  label: 'GitHub Runners',
  routes: [
    { index: true, Component: OverviewPage },
    { path: 'runners', crumb: 'Runners', Component: RunnersPage },
    { path: 'jobs', crumb: 'Jobs', Component: JobsPage },
    { path: 'pools', crumb: 'Pools', Component: PoolsPage },
    // Not limited to admins: everyone can read the setup state, the writes are disabled for them.
    { path: 'settings', crumb: 'Settings', Component: SettingsPage },
  ],
  contributions: {
    'nav.items': [
      {
        id: 'main',
        label: 'GitHub Runners',
        path: '',
        icon: ContainerIcon,
        children: [
          { id: 'overview', label: 'Overview', path: '' },
          { id: 'runners', label: 'Runners', path: 'runners' },
          { id: 'jobs', label: 'Jobs', path: 'jobs' },
          { id: 'pools', label: 'Pools', path: 'pools' },
          { id: 'settings', label: 'Settings', path: 'settings' },
        ],
      },
    ],
    'home.widgets': [
      { id: 'busy-runners', title: 'Busy runners', size: 'stat', component: BusyRunnersWidget },
    ],
    'cluster.widgets': [
      { id: 'cluster-runners', title: 'GitHub runners', component: ClusterRunnersWidget },
    ],
  },
});

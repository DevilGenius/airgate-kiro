import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AccountForm, type AccountFormProps } from './AccountForm';

function Harness({
  accountType,
  credentials = {},
  mode = 'create',
  oauth,
  onBatchImport,
  onBatchModeChange = vi.fn(),
  onChange = vi.fn(),
  onSuggestedName = vi.fn(),
  onTypeChange = vi.fn(),
}: {
  accountType?: string;
  credentials?: Record<string, string>;
  mode?: AccountFormProps['mode'];
  oauth?: AccountFormProps['oauth'];
  onBatchImport?: AccountFormProps['onBatchImport'];
  onBatchModeChange?: NonNullable<AccountFormProps['onBatchModeChange']>;
  onChange?: NonNullable<AccountFormProps['onChange']>;
  onSuggestedName?: NonNullable<AccountFormProps['onSuggestedName']>;
  onTypeChange?: NonNullable<AccountFormProps['onAccountTypeChange']>;
}) {
  const [currentCredentials, setCurrentCredentials] = useState(credentials);
  const [currentType, setCurrentType] = useState(accountType);

  return (
    <AccountForm
      accountType={currentType}
      credentials={currentCredentials}
      mode={mode}
      oauth={oauth}
      onBatchImport={onBatchImport}
      onBatchModeChange={onBatchModeChange}
      onSuggestedName={onSuggestedName}
      onAccountTypeChange={(next) => {
        setCurrentType(next);
        onTypeChange(next);
      }}
      onChange={(next) => {
        setCurrentCredentials(next);
        onChange(next);
      }}
    />
  );
}

function oauthBridge(overrides: Partial<NonNullable<AccountFormProps['oauth']>> = {}) {
  return {
    exchange: vi.fn(),
    start: vi.fn(),
    ...overrides,
  };
}

describe('Kiro AccountForm', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('switches to API key mode and updates key, region and usage-limit override', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onTypeChange = vi.fn();

    render(<Harness onChange={onChange} onTypeChange={onTypeChange} />);

    await user.click(screen.getByText('API Key'));
    await user.type(screen.getByPlaceholderText('ksk_...'), 'ksk-test');
    await user.type(screen.getByPlaceholderText('us-east-1'), 'us-west-2');
    await user.click(screen.getByLabelText(/无视用量限流/));

    expect(onTypeChange).toHaveBeenCalledWith('api_key');
    expect(onChange).toHaveBeenLastCalledWith({
      ignore_usage_limit: 'true',
      kiro_api_key: 'ksk-test',
      region: 'us-west-2',
    });
  });

  it('runs OAuth start and manual callback exchange flow', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onSuggestedName = vi.fn();
    const onTypeChange = vi.fn();
    const oauth = oauthBridge({
      exchange: vi.fn().mockResolvedValue({
        accountName: 'Kiro User',
        accountType: 'oauth',
        credentials: { refresh_token: 'refresh', region: 'us-east-1' },
      }),
      start: vi.fn().mockResolvedValue({ authorizeURL: 'https://kiro-auth.example', state: 'state-1' }),
    });

    render(
      <Harness
        oauth={oauth}
        onChange={onChange}
        onSuggestedName={onSuggestedName}
        onTypeChange={onTypeChange}
      />,
    );

    await user.click(screen.getByRole('button', { name: '生成授权链接' }));
    expect(await screen.findByDisplayValue('https://kiro-auth.example')).toBeTruthy();
    await user.type(screen.getByPlaceholderText(/oauth\/callback/), 'http://localhost/oauth/callback?code=ok');
    await user.click(screen.getByRole('button', { name: '完成授权' }));

    await waitFor(() => expect(oauth.exchange).toHaveBeenCalledWith('http://localhost/oauth/callback?code=ok'));
    expect(onTypeChange).toHaveBeenCalledWith('oauth');
    expect(onSuggestedName).toHaveBeenCalledWith('Kiro User');
    expect(onChange).toHaveBeenLastCalledWith({
      refresh_token: 'refresh',
      region: 'us-east-1',
    });
  });

  it('handles device auth challenge and completion', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const onSuggestedName = vi.fn();
    const oauth = oauthBridge({
      exchange: vi.fn()
        .mockResolvedValueOnce({
          accountName: '',
          accountType: '__device_auth__',
          credentials: {
            session_id: 'session-1',
            user_code: 'ABCD-1234',
            verification_uri: 'https://device.example',
          },
        })
        .mockResolvedValueOnce({
          accountName: 'Device Kiro',
          accountType: 'oauth',
          credentials: { refresh_token: 'device-refresh' },
        }),
      start: vi.fn().mockResolvedValue({ authorizeURL: 'https://kiro-auth.example', state: 'state-2' }),
    });

    render(<Harness oauth={oauth} onChange={onChange} onSuggestedName={onSuggestedName} />);

    await user.click(screen.getByRole('button', { name: '生成授权链接' }));
    await user.type(await screen.findByPlaceholderText(/oauth\/callback/), 'http://localhost/oauth/callback?device=1');
    await user.click(screen.getByRole('button', { name: '完成授权' }));

    expect(await screen.findByDisplayValue('https://device.example')).toBeTruthy();
    expect(screen.getByDisplayValue('ABCD-1234')).toBeTruthy();

    await user.click(screen.getByRole('button', { name: '我已完成授权' }));

    await waitFor(() => expect(oauth.exchange).toHaveBeenLastCalledWith('device-complete:session-1'));
    expect(onSuggestedName).toHaveBeenCalledWith('Device Kiro');
    expect(onChange).toHaveBeenLastCalledWith({ refresh_token: 'device-refresh' });
  });

  it('batch imports JSON refresh-token credentials', async () => {
    const user = userEvent.setup();
    const onBatchImport = vi.fn().mockResolvedValue({ failed: 1, imported: 2 });
    const onBatchModeChange = vi.fn();

    render(
      <Harness
        oauth={oauthBridge()}
        onBatchImport={onBatchImport}
        onBatchModeChange={onBatchModeChange}
      />,
    );

    await user.click(screen.getByRole('button', { name: '批量导入' }));
    fireEvent.change(screen.getByPlaceholderText(/粘贴 JSON 数组/), { target: { value: JSON.stringify([
      { name: 'Kiro One', refresh_token: 'rt-1', region: 'us-east-1' },
      { refresh_token: 'rt-2' },
      { name: 'Missing token' },
    ]) } });
    await user.click(screen.getByRole('button', { name: '开始导入' }));

    await waitFor(() => expect(onBatchImport).toHaveBeenCalledWith([
      {
        credentials: { refresh_token: 'rt-1', region: 'us-east-1' },
        name: 'Kiro One',
        type: 'oauth',
      },
      {
        credentials: { refresh_token: 'rt-2' },
        name: 'Kiro-2',
        type: 'oauth',
      },
    ]));
    expect(await screen.findByText('导入完成：成功 2 个，失败 1 个')).toBeTruthy();
    expect(onBatchModeChange).toHaveBeenCalledWith(true);
  });
});

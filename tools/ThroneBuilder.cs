using System;
using System.Diagnostics;
using System.Drawing;
using System.IO;
using System.Windows.Forms;

namespace ThroneBuilderApp
{
    internal static class Program
    {
        [STAThread]
        private static void Main()
        {
            Application.EnableVisualStyles();
            Application.SetCompatibleTextRenderingDefault(false);
            Application.Run(new BuilderForm());
        }
    }

    internal sealed class BuilderForm : Form
    {
        private readonly RadioButton coreMode;
        private readonly RadioButton sguardMode;
        private readonly Button buildButton;
        private readonly Button openButton;
        private readonly Label statusLabel;
        private Process activeBuild;

        public BuilderForm()
        {
            Text = "ThroneBuilder 1.3.0-beta.1";
            StartPosition = FormStartPosition.CenterScreen;
            FormBorderStyle = FormBorderStyle.FixedDialog;
            MaximizeBox = false;
            MinimizeBox = true;
            ClientSize = new Size(520, 250);
            Font = new Font("Segoe UI", 10F, FontStyle.Regular, GraphicsUnit.Point);

            var title = new Label
            {
                AutoSize = false,
                Text = "Select build output",
                Font = new Font("Segoe UI Semibold", 14F, FontStyle.Bold, GraphicsUnit.Point),
                Location = new Point(24, 20),
                Size = new Size(470, 32)
            };

            coreMode = new RadioButton
            {
                Text = "ThroneCore - 3 target folders",
                Location = new Point(30, 70),
                Size = new Size(450, 28),
                Checked = true
            };

            sguardMode = new RadioButton
            {
                Text = "SGuard - 4 files for IRSpeedyVPN",
                Location = new Point(30, 105),
                Size = new Size(450, 28)
            };

            buildButton = new Button
            {
                Text = "Build",
                Location = new Point(30, 155),
                Size = new Size(150, 40)
            };
            buildButton.Click += BuildButton_Click;

            openButton = new Button
            {
                Text = "Open output",
                Location = new Point(195, 155),
                Size = new Size(150, 40)
            };
            openButton.Click += OpenButton_Click;

            statusLabel = new Label
            {
                AutoSize = false,
                Text = "Ready",
                Location = new Point(30, 207),
                Size = new Size(460, 28)
            };

            Controls.Add(title);
            Controls.Add(coreMode);
            Controls.Add(sguardMode);
            Controls.Add(buildButton);
            Controls.Add(openButton);
            Controls.Add(statusLabel);
        }

        private string RepoRoot
        {
            get { return AppDomain.CurrentDomain.BaseDirectory.TrimEnd(Path.DirectorySeparatorChar, Path.AltDirectorySeparatorChar); }
        }

        private string SelectedMode
        {
            get { return sguardMode.Checked ? "--sguard" : "--core"; }
        }

        private string SelectedOutput
        {
            get { return Path.Combine(RepoRoot, sguardMode.Checked ? "SGuardBuilds" : "CoreBuilds"); }
        }

        private void BuildButton_Click(object sender, EventArgs e)
        {
            if (activeBuild != null && !activeBuild.HasExited)
            {
                MessageBox.Show(this, "A build is already running.", "ThroneBuilder", MessageBoxButtons.OK, MessageBoxIcon.Information);
                return;
            }

            var buildCmd = Path.Combine(RepoRoot, "build-core.cmd");
            if (!File.Exists(buildCmd))
            {
                MessageBox.Show(this, "build-core.cmd was not found next to ThroneBuilder.exe.", "ThroneBuilder", MessageBoxButtons.OK, MessageBoxIcon.Error);
                return;
            }

            var psi = new ProcessStartInfo
            {
                FileName = "cmd.exe",
                Arguments = "/c \"\"" + buildCmd + "\" " + SelectedMode + "\"",
                WorkingDirectory = RepoRoot,
                UseShellExecute = true,
                CreateNoWindow = false
            };

            try
            {
                activeBuild = Process.Start(psi);
                if (activeBuild == null)
                    throw new InvalidOperationException("Could not start the build process.");

                activeBuild.EnableRaisingEvents = true;
                activeBuild.Exited += ActiveBuild_Exited;
                buildButton.Enabled = false;
                statusLabel.Text = sguardMode.Checked ? "Building 4 SGuard files..." : "Building 3 ThroneCore targets...";
            }
            catch (Exception ex)
            {
                activeBuild = null;
                MessageBox.Show(this, ex.Message, "ThroneBuilder", MessageBoxButtons.OK, MessageBoxIcon.Error);
            }
        }

        private void ActiveBuild_Exited(object sender, EventArgs e)
        {
            var process = activeBuild;
            var exitCode = process == null ? -1 : process.ExitCode;
            BeginInvoke((Action)(() =>
            {
                buildButton.Enabled = true;
                statusLabel.Text = exitCode == 0 ? "Build completed." : "Build failed. Exit code: " + exitCode;
            }));
        }

        private void OpenButton_Click(object sender, EventArgs e)
        {
            var output = SelectedOutput;
            if (!Directory.Exists(output))
            {
                MessageBox.Show(this, "Output folder does not exist yet:\r\n" + output, "ThroneBuilder", MessageBoxButtons.OK, MessageBoxIcon.Information);
                return;
            }

            Process.Start(new ProcessStartInfo
            {
                FileName = "explorer.exe",
                Arguments = "\"" + output + "\"",
                UseShellExecute = true
            });
        }
    }
}

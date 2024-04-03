|          **Trigger**         |                   **Task**                  |
|------------------------------|---------------------------------------------|
| Fleet template updated       | Create new TemplateVersion                  |
| Fleet created                | Create new TemplateVersion                  |
| TemplateVersion created      | Populate the TemplateVersion's Status       |
| TemplateVersion set as valid | Roll out TemplateVersion to Fleet's devices |
| Device created               | Roll out latest TemplateVersion to device   |
| Device owner updated         | Roll out latest TemplateVersion to device   |
| Fleet created                | Update device ownership as necessary        |
| Fleet selector updated       | Update device ownership as necessary        |
| Fleet deleted                | Update device ownership as necessary        |
| Device created               | Update device ownership as necessary        |
| Device labels updated        | Update device ownership as necessary        |
| Device deleted               | Update device ownership as necessary        |
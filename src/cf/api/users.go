package api

import (
	"cf"
	"cf/configuration"
	"cf/errors"
	"cf/models"
	"cf/net"
	"fmt"
	neturl "net/url"
	"strings"
)

type UserResource struct {
	Resource
	Entity UserEntity
}

type UAAUserResources struct {
	Resources []struct {
		Id       string
		Username string
	}
}

func (resource UserResource) ToFields() models.UserFields {
	return models.UserFields{
		Guid:    resource.Metadata.Guid,
		IsAdmin: resource.Entity.Admin,
	}
}

type UserEntity struct {
	Entity
	Admin bool
}

var orgRoleToPathMap = map[string]string{
	models.ORG_USER:        "users",
	models.ORG_MANAGER:     "managers",
	models.BILLING_MANAGER: "billing_managers",
	models.ORG_AUDITOR:     "auditors",
}

var spaceRoleToPathMap = map[string]string{
	models.SPACE_MANAGER:   "managers",
	models.SPACE_DEVELOPER: "developers",
	models.SPACE_AUDITOR:   "auditors",
}

type UserRepository interface {
	FindByUsername(username string) (user models.UserFields, apiResponse errors.Error)
	ListUsersInOrgForRole(orgGuid string, role string) ([]models.UserFields, errors.Error)
	ListUsersInSpaceForRole(spaceGuid string, role string) ([]models.UserFields, errors.Error)
	Create(username, password string) (apiResponse errors.Error)
	Delete(userGuid string) (apiResponse errors.Error)
	SetOrgRole(userGuid, orgGuid, role string) (apiResponse errors.Error)
	UnsetOrgRole(userGuid, orgGuid, role string) (apiResponse errors.Error)
	SetSpaceRole(userGuid, spaceGuid, orgGuid, role string) (apiResponse errors.Error)
	UnsetSpaceRole(userGuid, spaceGuid, role string) (apiResponse errors.Error)
}

type CloudControllerUserRepository struct {
	config       configuration.Reader
	uaaGateway   net.Gateway
	ccGateway    net.Gateway
	endpointRepo EndpointRepository
}

func NewCloudControllerUserRepository(config configuration.Reader, uaaGateway net.Gateway, ccGateway net.Gateway, endpointRepo EndpointRepository) (repo CloudControllerUserRepository) {
	repo.config = config
	repo.uaaGateway = uaaGateway
	repo.ccGateway = ccGateway
	repo.endpointRepo = endpointRepo
	return
}

func (repo CloudControllerUserRepository) FindByUsername(username string) (user models.UserFields, apiResponse errors.Error) {
	uaaEndpoint, apiResponse := repo.endpointRepo.GetUAAEndpoint()
	if apiResponse != nil {
		return
	}

	usernameFilter := neturl.QueryEscape(fmt.Sprintf(`userName Eq "%s"`, username))
	path := fmt.Sprintf("%s/Users?attributes=id,userName&filter=%s", uaaEndpoint, usernameFilter)

	users, apiResponse := repo.updateOrFindUsersWithUAAPath([]models.UserFields{}, path)
	if len(users) == 0 {
		apiResponse = errors.NewNotFoundError("User %s not found", username)
		return
	}

	user = users[0]
	return
}

func (repo CloudControllerUserRepository) ListUsersInOrgForRole(orgGuid string, roleName string) (users []models.UserFields, apiResponse errors.Error) {
	return repo.listUsersWithPath(fmt.Sprintf("/v2/organizations/%s/%s", orgGuid, orgRoleToPathMap[roleName]))
}

func (repo CloudControllerUserRepository) ListUsersInSpaceForRole(spaceGuid string, roleName string) (users []models.UserFields, apiResponse errors.Error) {
	return repo.listUsersWithPath(fmt.Sprintf("/v2/spaces/%s/%s", spaceGuid, spaceRoleToPathMap[roleName]))
}

func (repo CloudControllerUserRepository) listUsersWithPath(path string) (users []models.UserFields, apiResponse errors.Error) {
	guidFilters := []string{}

	apiResponse = repo.ccGateway.ListPaginatedResources(
		repo.config.ApiEndpoint(),
		repo.config.AccessToken(),
		path,
		UserResource{},
		func(resource interface{}) bool {
			user := resource.(UserResource).ToFields()
			users = append(users, user)
			guidFilters = append(guidFilters, fmt.Sprintf(`Id eq "%s"`, user.Guid))
			return true
		})
	if apiResponse != nil {
		return
	}

	uaaEndpoint, apiResponse := repo.endpointRepo.GetUAAEndpoint()
	if apiResponse != nil {
		return
	}

	filter := strings.Join(guidFilters, " or ")
	usersURL := fmt.Sprintf("%s/Users?attributes=id,userName&filter=%s", uaaEndpoint, neturl.QueryEscape(filter))
	users, apiResponse = repo.updateOrFindUsersWithUAAPath(users, usersURL)
	return
}

func (repo CloudControllerUserRepository) updateOrFindUsersWithUAAPath(ccUsers []models.UserFields, path string) (updatedUsers []models.UserFields, apiResponse errors.Error) {
	uaaResponse := new(UAAUserResources)
	apiResponse = repo.uaaGateway.GetResource(path, repo.config.AccessToken(), uaaResponse)
	if apiResponse != nil {
		return
	}

	for _, uaaResource := range uaaResponse.Resources {
		var ccUserFields models.UserFields

		for _, u := range ccUsers {
			if u.Guid == uaaResource.Id {
				ccUserFields = u
				break
			}
		}

		updatedUsers = append(updatedUsers, models.UserFields{
			Guid:     uaaResource.Id,
			Username: uaaResource.Username,
			IsAdmin:  ccUserFields.IsAdmin,
		})
	}
	return
}

func (repo CloudControllerUserRepository) Create(username, password string) (apiResponse errors.Error) {
	uaaEndpoint, apiResponse := repo.endpointRepo.GetUAAEndpoint()
	if apiResponse != nil {
		return
	}

	path := fmt.Sprintf("%s/Users", uaaEndpoint)
	body := fmt.Sprintf(`{
  "userName": "%s",
  "emails": [{"value":"%s"}],
  "password": "%s",
  "name": {"givenName":"%s", "familyName":"%s"}
}`,
		username,
		username,
		password,
		username,
		username,
	)
	request, apiResponse := repo.uaaGateway.NewRequest("POST", path, repo.config.AccessToken(), strings.NewReader(body))
	if apiResponse != nil {
		return
	}

	type uaaUserFields struct {
		Id string
	}
	createUserResponse := &uaaUserFields{}

	_, apiResponse = repo.uaaGateway.PerformRequestForJSONResponse(request, createUserResponse)
	if apiResponse != nil {
		return
	}

	path = fmt.Sprintf("%s/v2/users", repo.config.ApiEndpoint())
	body = fmt.Sprintf(`{"guid":"%s"}`, createUserResponse.Id)
	return repo.ccGateway.CreateResource(path, repo.config.AccessToken(), strings.NewReader(body))
}

func (repo CloudControllerUserRepository) Delete(userGuid string) (apiResponse errors.Error) {
	path := fmt.Sprintf("%s/v2/users/%s", repo.config.ApiEndpoint(), userGuid)

	apiResponse = repo.ccGateway.DeleteResource(path, repo.config.AccessToken())
	if apiResponse != nil && apiResponse.ErrorCode() != cf.USER_NOT_FOUND {
		return
	}

	uaaEndpoint, apiResponse := repo.endpointRepo.GetUAAEndpoint()
	if apiResponse != nil {
		return
	}

	path = fmt.Sprintf("%s/Users/%s", uaaEndpoint, userGuid)
	return repo.uaaGateway.DeleteResource(path, repo.config.AccessToken())
}

func (repo CloudControllerUserRepository) SetOrgRole(userGuid string, orgGuid string, role string) (apiResponse errors.Error) {
	apiResponse = repo.setOrUnsetOrgRole("PUT", userGuid, orgGuid, role)
	if apiResponse != nil {
		return
	}
	return repo.addOrgUserRole(userGuid, orgGuid)
}

func (repo CloudControllerUserRepository) UnsetOrgRole(userGuid, orgGuid, role string) (apiResponse errors.Error) {
	return repo.setOrUnsetOrgRole("DELETE", userGuid, orgGuid, role)
}

func (repo CloudControllerUserRepository) setOrUnsetOrgRole(verb, userGuid, orgGuid, role string) (apiResponse errors.Error) {
	rolePath, found := orgRoleToPathMap[role]

	if !found {
		apiResponse = errors.NewErrorWithMessage("Invalid Role %s", role)
		return
	}

	path := fmt.Sprintf("%s/v2/organizations/%s/%s/%s", repo.config.ApiEndpoint(), orgGuid, rolePath, userGuid)

	request, apiResponse := repo.ccGateway.NewRequest(verb, path, repo.config.AccessToken(), nil)
	if apiResponse != nil {
		return
	}

	apiResponse = repo.ccGateway.PerformRequest(request)
	if apiResponse != nil {
		return
	}
	return
}

func (repo CloudControllerUserRepository) SetSpaceRole(userGuid, spaceGuid, orgGuid, role string) (apiResponse errors.Error) {
	rolePath, apiResponse := repo.checkSpaceRole(userGuid, spaceGuid, role)
	if apiResponse != nil {
		return
	}

	apiResponse = repo.addOrgUserRole(userGuid, orgGuid)
	if apiResponse != nil {
		return
	}

	return repo.ccGateway.UpdateResource(rolePath, repo.config.AccessToken(), nil)
}

func (repo CloudControllerUserRepository) UnsetSpaceRole(userGuid, spaceGuid, role string) (apiResponse errors.Error) {
	rolePath, apiResponse := repo.checkSpaceRole(userGuid, spaceGuid, role)
	if apiResponse != nil {
		return
	}
	return repo.ccGateway.DeleteResource(rolePath, repo.config.AccessToken())
}

func (repo CloudControllerUserRepository) checkSpaceRole(userGuid, spaceGuid, role string) (fullPath string, apiResponse errors.Error) {
	rolePath, found := spaceRoleToPathMap[role]

	if !found {
		apiResponse = errors.NewErrorWithMessage("Invalid Role %s", role)
	}

	fullPath = fmt.Sprintf("%s/v2/spaces/%s/%s/%s", repo.config.ApiEndpoint(), spaceGuid, rolePath, userGuid)
	return
}

func (repo CloudControllerUserRepository) addOrgUserRole(userGuid, orgGuid string) (apiResponse errors.Error) {
	path := fmt.Sprintf("%s/v2/organizations/%s/users/%s", repo.config.ApiEndpoint(), orgGuid, userGuid)
	return repo.ccGateway.UpdateResource(path, repo.config.AccessToken(), nil)
}
